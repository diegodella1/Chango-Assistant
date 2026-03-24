package emailwatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// EmailSummary represents a classified email.
type EmailSummary struct {
	ID             string `json:"id"`
	From           string `json:"from"`
	Subject        string `json:"subject"`
	Preview        string `json:"preview"`
	Date           string `json:"date"`
	Classification string `json:"classification"`
}

// seenState tracks processed message IDs.
type seenState struct {
	SeenIDs   map[string]time.Time `json:"seen_ids"`
	LastCheck time.Time            `json:"last_check"`
}

// Service monitors the Gmail inbox, classifies emails with a local LLM,
// and notifies or auto-responds based on classification.
type Service struct {
	saFile    string
	email     string
	bus       *bus.MessageBus
	state     *state.Manager
	workspace string
	local     providers.LLMProvider
	interval  time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
}

// NewService creates a new email watcher service.
// If saFile or email are empty, Start() will return immediately.
func NewService(saFile, email, workspace string, stateMgr *state.Manager, local providers.LLMProvider) *Service {
	return &Service{
		saFile:    saFile,
		email:     email,
		workspace: workspace,
		state:     stateMgr,
		local:     local,
		interval:  30 * time.Minute,
	}
}

// SetBus sets the message bus for sending notifications.
func (s *Service) SetBus(b *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = b
}

// Start begins the email check loop. Blocks until context is cancelled.
func (s *Service) Start(ctx context.Context) {
	if s.saFile == "" || s.email == "" {
		logger.InfoC("emailwatch", "Email watcher disabled (no service account or email configured)")
		return
	}

	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	logger.InfoC("emailwatch", "Email watcher started (interval: 30m)")

	// Initial delay to let other services start
	timer := time.NewTimer(45 * time.Second)
	select {
	case <-s.ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}

	s.checkInbox()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkInbox()
		}
	}
}

// Stop stops the email watcher.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("emailwatch", "Email watcher stopped")
}

// checkInbox fetches unread emails, classifies them, and takes action.
func (s *Service) checkInbox() {
	logger.DebugCF("emailwatch", "Checking inbox", nil)

	srv, err := s.createGmailService()
	if err != nil {
		logger.ErrorCF("emailwatch", "Failed to create Gmail service", map[string]interface{}{"error": err.Error()})
		return
	}

	seen := s.loadSeen()

	// List unread messages
	list, err := srv.Users.Messages.List("me").Q("is:unread").MaxResults(20).Do()
	if err != nil {
		logger.ErrorCF("emailwatch", "Failed to list messages", map[string]interface{}{"error": err.Error()})
		return
	}

	if len(list.Messages) == 0 {
		logger.DebugCF("emailwatch", "No unread messages", nil)
		seen.LastCheck = time.Now()
		s.saveSeen(seen)
		return
	}

	var dailyLog []EmailSummary
	newSeen := false

	for _, m := range list.Messages {
		if _, already := seen.SeenIDs[m.Id]; already {
			continue
		}

		summary, err := s.fetchEmailSummary(srv, m.Id)
		if err != nil {
			logger.ErrorCF("emailwatch", "Failed to fetch email", map[string]interface{}{
				"id":    m.Id,
				"error": err.Error(),
			})
			continue
		}

		// Classify with local LLM
		summary.Classification = s.classifyEmail(summary)

		logger.InfoCF("emailwatch", "Email classified", map[string]interface{}{
			"id":             summary.ID,
			"from":           summary.From,
			"subject":        summary.Subject,
			"classification": summary.Classification,
		})

		switch {
		case summary.Classification == "URGENT":
			s.notifyUrgent(summary)
			dailyLog = append(dailyLog, *summary)

		case strings.HasPrefix(summary.Classification, "AUTO_REPLY:"):
			draft := strings.TrimPrefix(summary.Classification, "AUTO_REPLY:")
			draft = strings.TrimSpace(draft)
			if draft != "" {
				s.sendAutoReply(srv, m.Id, summary, draft)
			}
			summary.Classification = "auto_reply"
			dailyLog = append(dailyLog, *summary)

		case summary.Classification == "NORMAL":
			dailyLog = append(dailyLog, *summary)

		case summary.Classification == "SPAM":
			// Ignore
		}

		seen.SeenIDs[m.Id] = time.Now()
		newSeen = true
	}

	// Prune old seen IDs (keep last 7 days)
	cutoff := time.Now().AddDate(0, 0, -7)
	for id, t := range seen.SeenIDs {
		if t.Before(cutoff) {
			delete(seen.SeenIDs, id)
		}
	}

	seen.LastCheck = time.Now()
	if newSeen {
		s.saveSeen(seen)
	}

	if len(dailyLog) > 0 {
		s.appendDailyLog(dailyLog)
	}
}

// createGmailService creates an authenticated Gmail API service.
func (s *Service) createGmailService() (*gmail.Service, error) {
	creds, err := os.ReadFile(s.saFile)
	if err != nil {
		return nil, fmt.Errorf("reading service account file: %w", err)
	}

	config, err := google.JWTConfigFromJSON(creds, gmail.GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parsing service account JSON: %w", err)
	}

	config.Subject = s.email
	client := config.Client(s.ctx)
	return gmail.NewService(s.ctx, option.WithHTTPClient(client))
}

// fetchEmailSummary gets headers and snippet for a message.
func (s *Service) fetchEmailSummary(srv *gmail.Service, id string) (*EmailSummary, error) {
	msg, err := srv.Users.Messages.Get("me", id).Format("metadata").
		MetadataHeaders("From", "Subject", "Date").Do()
	if err != nil {
		return nil, err
	}

	summary := &EmailSummary{ID: id}
	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "From":
			summary.From = h.Value
		case "Subject":
			summary.Subject = h.Value
		case "Date":
			summary.Date = h.Value
		}
	}

	summary.Preview = msg.Snippet
	if len(summary.Preview) > 200 {
		summary.Preview = summary.Preview[:200]
	}

	return summary, nil
}

// classifyEmail uses the local LLM to classify an email.
// Falls back to "NORMAL" if no local provider is available.
func (s *Service) classifyEmail(summary *EmailSummary) string {
	if s.local == nil {
		return "NORMAL"
	}

	prompt := fmt.Sprintf(
		`Classify this email. Reply with EXACTLY one of: URGENT, NORMAL, SPAM, or AUTO_REPLY:<draft response>

From: %s
Subject: %s
Preview: %s

Rules:
- URGENT: time-sensitive, requires immediate human attention (payments, security, deadlines)
- NORMAL: regular email, informational
- SPAM: marketing, newsletters, automated notifications
- AUTO_REPLY: simple emails that can be answered with a brief, polite response (meeting confirmations, simple questions with obvious answers)

Reply ONLY with the classification (and draft if AUTO_REPLY).`,
		summary.From, summary.Subject, summary.Preview,
	)

	msgs := []providers.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := s.local.Chat(s.ctx, msgs, nil, s.local.GetDefaultModel(), map[string]interface{}{
		"max_tokens":  150,
		"temperature": 0.1,
	})
	if err != nil {
		logger.ErrorCF("emailwatch", "LLM classification failed", map[string]interface{}{"error": err.Error()})
		return "NORMAL"
	}

	result := strings.TrimSpace(resp.Content)

	// Validate classification
	upper := strings.ToUpper(result)
	if upper == "URGENT" || upper == "NORMAL" || upper == "SPAM" {
		return upper
	}
	if strings.HasPrefix(upper, "AUTO_REPLY:") {
		// Keep original casing for the draft part
		return "AUTO_REPLY:" + strings.TrimPrefix(result, result[:len("AUTO_REPLY:")])
	}

	return "NORMAL"
}

// notifyUrgent sends a Telegram notification for urgent emails.
func (s *Service) notifyUrgent(summary *EmailSummary) {
	s.mu.Lock()
	msgBus := s.bus
	s.mu.Unlock()

	if msgBus == nil {
		return
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}

	platform, userID := parseLastChannel(lastChannel)
	if platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		return
	}

	msg := fmt.Sprintf("\U0001F4E7 Email urgente de %s: %s", summary.From, summary.Subject)
	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: msg,
	})

	logger.InfoCF("emailwatch", "Urgent email notification sent", map[string]interface{}{
		"from":    summary.From,
		"subject": summary.Subject,
		"to":      platform,
	})
}

// sendAutoReply sends an auto-reply via Gmail API.
func (s *Service) sendAutoReply(srv *gmail.Service, msgID string, summary *EmailSummary, draft string) {
	// Get original message for threading headers
	orig, err := srv.Users.Messages.Get("me", msgID).Format("metadata").
		MetadataHeaders("From", "Subject", "Message-Id", "References", "In-Reply-To").Do()
	if err != nil {
		logger.ErrorCF("emailwatch", "Failed to get original for reply", map[string]interface{}{"error": err.Error()})
		return
	}

	var from, subject, messageID, references string
	for _, h := range orig.Payload.Headers {
		switch h.Name {
		case "From":
			from = h.Value
		case "Subject":
			subject = h.Value
		case "Message-Id":
			messageID = h.Value
		case "References":
			references = h.Value
		}
	}

	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	refs := references
	if refs != "" {
		refs += " " + messageID
	} else {
		refs = messageID
	}

	var raw strings.Builder
	raw.WriteString(fmt.Sprintf("To: %s\r\n", from))
	raw.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	raw.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", messageID))
	raw.WriteString(fmt.Sprintf("References: %s\r\n", refs))
	raw.WriteString("MIME-Version: 1.0\r\n")
	raw.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
	raw.WriteString(draft)

	import64 := encodeBase64URL([]byte(raw.String()))
	msg := &gmail.Message{
		Raw:      import64,
		ThreadId: orig.ThreadId,
	}

	if _, err := srv.Users.Messages.Send("me", msg).Do(); err != nil {
		logger.ErrorCF("emailwatch", "Failed to send auto-reply", map[string]interface{}{
			"to":    from,
			"error": err.Error(),
		})
		return
	}

	logger.InfoCF("emailwatch", "Auto-reply sent", map[string]interface{}{
		"to":      from,
		"subject": subject,
	})
}

// loadSeen reads the seen message IDs from disk.
func (s *Service) loadSeen() *seenState {
	st := &seenState{SeenIDs: make(map[string]time.Time)}

	path := filepath.Join(s.workspace, "state", "email_seen.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}

	if err := json.Unmarshal(data, st); err != nil {
		logger.ErrorCF("emailwatch", "Failed to parse email_seen.json", map[string]interface{}{"error": err.Error()})
		return &seenState{SeenIDs: make(map[string]time.Time)}
	}

	if st.SeenIDs == nil {
		st.SeenIDs = make(map[string]time.Time)
	}

	return st
}

// saveSeen persists the seen state atomically.
func (s *Service) saveSeen(st *seenState) {
	stateDir := filepath.Join(s.workspace, "state")
	os.MkdirAll(stateDir, 0755)

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		logger.ErrorCF("emailwatch", "Failed to marshal seen state", map[string]interface{}{"error": err.Error()})
		return
	}

	filePath := filepath.Join(stateDir, "email_seen.json")
	tmpPath := filePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("emailwatch", "Failed to write seen state", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		logger.ErrorCF("emailwatch", "Failed to rename seen state", map[string]interface{}{"error": err.Error()})
	}
}

// appendDailyLog appends email summaries to today's log file.
func (s *Service) appendDailyLog(emails []EmailSummary) {
	logDir := filepath.Join(s.workspace, "state", "email_logs")
	os.MkdirAll(logDir, 0755)

	today := time.Now().Format("2006-01-02")
	logPath := filepath.Join(logDir, today+".json")

	var existing []EmailSummary
	if data, err := os.ReadFile(logPath); err == nil {
		json.Unmarshal(data, &existing)
	}

	existing = append(existing, emails...)

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return
	}

	tmpPath := logPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmpPath, logPath); err != nil {
		os.Remove(tmpPath)
	}
}

// parseLastChannel splits "platform:userID" into parts.
func parseLastChannel(lastChannel string) (platform, userID string) {
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// encodeBase64URL encodes bytes to URL-safe base64.
func encodeBase64URL(data []byte) string {
	return base64.URLEncoding.EncodeToString(data)
}
