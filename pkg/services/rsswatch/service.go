package rsswatch

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"
)

// RSSItem represents a single feed item with relevance classification.
type RSSItem struct {
	Title     string `json:"title"`
	Link      string `json:"link"`
	Published string `json:"published"`
	Summary   string `json:"summary"` // first 200 chars
	Feed      string `json:"feed"`
	Relevant  bool   `json:"relevant"`
}

// Service monitors RSS/Atom feeds and filters items by relevance.
type Service struct {
	cfg       config.RSSConfig
	bus       *bus.MessageBus
	state     *state.Manager
	workspace string
	local     providers.LLMProvider // Qwen local for classification
	seen      map[string]bool      // URLs already processed
	digest    []RSSItem            // today's relevant items
	lastDay   string               // track day changes for digest delivery
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
}

const maxSeenURLs = 1000

// NewService creates a new RSS reader service.
func NewService(cfg config.RSSConfig, workspace string, stateMgr *state.Manager, local providers.LLMProvider) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = 14400
	}
	return &Service{
		cfg:       cfg,
		state:     stateMgr,
		workspace: workspace,
		local:     local,
		seen:      make(map[string]bool),
		digest:    nil,
	}
}

// SetBus sets the message bus for sending digests.
func (s *Service) SetBus(b *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = b
}

// Start begins the RSS polling loop.
func (s *Service) Start(ctx context.Context) {
	if !s.cfg.Enabled || len(s.cfg.Feeds) == 0 {
		logger.InfoC("rsswatch", "RSS service disabled or no feeds configured")
		return
	}

	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	// Load previously seen URLs
	s.loadSeen()
	s.loadDigest()
	s.lastDay = time.Now().Format("2006-01-02")

	logger.InfoC("rsswatch", "RSS reader service started")

	// Run immediately on start
	s.fetchAll()

	ticker := time.NewTicker(time.Duration(s.cfg.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.fetchAll()
		}
	}
}

// Stop stops the RSS service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("rsswatch", "RSS reader service stopped")
}

func (s *Service) fetchAll() {
	// Check if day changed — deliver digest and reset
	today := time.Now().Format("2006-01-02")
	s.mu.Lock()
	dayChanged := today != s.lastDay
	if dayChanged {
		s.lastDay = today
	}
	s.mu.Unlock()

	if dayChanged {
		s.deliverDigest()
	}

	client := &http.Client{Timeout: 30 * time.Second}
	var newItems int

	for _, feed := range s.cfg.Feeds {
		items, err := s.fetchFeed(client, feed)
		if err != nil {
			logger.WarnCF("rsswatch", "Failed to fetch feed", map[string]interface{}{
				"feed":  feed.Name,
				"error": err.Error(),
			})
			continue
		}

		for _, item := range items {
			s.mu.Lock()
			alreadySeen := s.seen[item.Link]
			s.mu.Unlock()

			if alreadySeen {
				continue
			}

			item.Feed = feed.Name
			item.Relevant = s.classifyRelevance(item)

			s.mu.Lock()
			s.seen[item.Link] = true
			if item.Relevant {
				s.digest = append(s.digest, item)
			}
			s.mu.Unlock()

			newItems++
		}
	}

	// Persist state
	s.saveSeen()
	s.saveDigest()

	logger.DebugCF("rsswatch", "RSS fetch round complete", map[string]interface{}{
		"feeds":     len(s.cfg.Feeds),
		"new_items": newItems,
	})
}

func (s *Service) fetchFeed(client *http.Client, feed config.RSSFeed) ([]RSSItem, error) {
	resp, err := client.Get(feed.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, err
	}

	return parseFeed(body)
}

// parseFeed handles both RSS 2.0 (<item>) and Atom (<entry>) formats.
func parseFeed(data []byte) ([]RSSItem, error) {
	// Try RSS 2.0 first
	var rss rssDoc
	if err := xml.Unmarshal(data, &rss); err == nil && len(rss.Channel.Items) > 0 {
		var items []RSSItem
		for _, ri := range rss.Channel.Items {
			items = append(items, RSSItem{
				Title:     ri.Title,
				Link:      ri.Link,
				Published: ri.PubDate,
				Summary:   truncate(stripTags(ri.Description), 200),
			})
		}
		return items, nil
	}

	// Try Atom
	var atom atomDoc
	if err := xml.Unmarshal(data, &atom); err == nil && len(atom.Entries) > 0 {
		var items []RSSItem
		for _, ae := range atom.Entries {
			var link string
			for _, l := range ae.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			if link == "" && len(ae.Links) > 0 {
				link = ae.Links[0].Href
			}
			items = append(items, RSSItem{
				Title:     ae.Title,
				Link:      link,
				Published: ae.Updated,
				Summary:   truncate(stripTags(ae.Summary), 200),
			})
		}
		return items, nil
	}

	return nil, fmt.Errorf("could not parse feed as RSS or Atom")
}

// XML structures for RSS 2.0
type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// XML structures for Atom
type atomDoc struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string     `xml:"title"`
	Links   []atomLink `xml:"link"`
	Summary string     `xml:"summary"`
	Updated string     `xml:"updated"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

func (s *Service) classifyRelevance(item RSSItem) bool {
	if len(s.cfg.Interests) == 0 {
		return true // no filter = everything relevant
	}

	// Try local LLM classification first
	if s.local != nil {
		return s.classifyWithLLM(item)
	}

	// Fallback: keyword matching
	return s.classifyWithKeywords(item)
}

func (s *Service) classifyWithLLM(item RSSItem) bool {
	interests := strings.Join(s.cfg.Interests, ", ")
	prompt := fmt.Sprintf("Is '%s - %s' relevant to [%s]? Answer YES or NO only.", item.Title, item.Summary, interests)

	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	msgs := []providers.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := s.local.Chat(ctx, msgs, nil, "", map[string]interface{}{
		"max_tokens":  4,
		"temperature": 0.1,
	})
	if err != nil {
		logger.DebugCF("rsswatch", "LLM classification failed, falling back to keywords", map[string]interface{}{
			"error": err.Error(),
		})
		return s.classifyWithKeywords(RSSItem{Title: item.Title, Summary: item.Summary})
	}

	answer := strings.TrimSpace(strings.ToUpper(resp.Content))
	return strings.HasPrefix(answer, "YES")
}

func (s *Service) classifyWithKeywords(item RSSItem) bool {
	text := strings.ToLower(item.Title + " " + item.Summary)
	for _, interest := range s.cfg.Interests {
		if strings.Contains(text, strings.ToLower(interest)) {
			return true
		}
	}
	return false
}

func (s *Service) deliverDigest() {
	s.mu.Lock()
	digest := make([]RSSItem, len(s.digest))
	copy(digest, s.digest)
	s.digest = nil // reset for new day
	msgBus := s.bus
	s.mu.Unlock()

	if len(digest) == 0 || msgBus == nil {
		return
	}

	// Build summary message
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📰 RSS Digest (%d items):\n\n", len(digest)))
	for i, item := range digest {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("... y %d más\n", len(digest)-10))
			break
		}
		sb.WriteString(fmt.Sprintf("• %s\n  %s\n  (%s)\n\n", item.Title, item.Link, item.Feed))
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}

	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return
	}
	platform, userID := parts[0], parts[1]
	if constants.IsInternalChannel(platform) {
		return
	}

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: sb.String(),
	})

	logger.InfoCF("rsswatch", "Digest delivered", map[string]interface{}{
		"items": len(digest),
		"to":    platform,
	})
}

// Persistence helpers

func (s *Service) loadSeen() {
	path := filepath.Join(s.workspace, "state", "rss_seen.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var urls []string
	if err := json.Unmarshal(data, &urls); err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range urls {
		s.seen[u] = true
	}
}

func (s *Service) saveSeen() {
	s.mu.Lock()
	urls := make([]string, 0, len(s.seen))
	for u := range s.seen {
		urls = append(urls, u)
	}
	// Keep only last N
	if len(urls) > maxSeenURLs {
		urls = urls[len(urls)-maxSeenURLs:]
		// Rebuild map
		s.seen = make(map[string]bool, maxSeenURLs)
		for _, u := range urls {
			s.seen[u] = true
		}
	}
	s.mu.Unlock()

	stateDir := filepath.Join(s.workspace, "state")
	os.MkdirAll(stateDir, 0755)

	data, err := json.Marshal(urls)
	if err != nil {
		return
	}

	path := filepath.Join(stateDir, "rss_seen.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
	}
}

func (s *Service) loadDigest() {
	path := filepath.Join(s.workspace, "state", "rss_digest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	json.Unmarshal(data, &s.digest)
}

func (s *Service) saveDigest() {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.digest, "", "  ")
	s.mu.Unlock()

	if err != nil {
		return
	}

	stateDir := filepath.Join(s.workspace, "state")
	os.MkdirAll(stateDir, 0755)

	path := filepath.Join(stateDir, "rss_digest.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
	}
}

// Utility functions

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func stripTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			result.WriteRune(r)
		}
	}
	return strings.TrimSpace(result.String())
}
