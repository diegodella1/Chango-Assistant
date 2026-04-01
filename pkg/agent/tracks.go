package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type TopicTrack struct {
	Topic       string    `json:"topic"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Mentions    int       `json:"mentions"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	LastMessage string    `json:"last_message,omitempty"`
}

func updateTopicTracks(workspace, current string, history []providers.Message) {
	topics := detectRecurringTopics(current, history, 3)
	if len(topics) == 0 {
		return
	}

	tracks := loadTopicTracks(workspace)
	byTopic := make(map[string]*TopicTrack, len(tracks))
	for i := range tracks {
		track := &tracks[i]
		byTopic[track.Topic] = track
	}

	now := time.Now()
	for _, topic := range topics {
		track, ok := byTopic[topic]
		if !ok {
			tracks = append(tracks, TopicTrack{
				Topic:       topic,
				Kind:        "recurring_topic",
				Status:      "active",
				Mentions:    1,
				FirstSeenAt: now,
				LastSeenAt:  now,
				LastMessage: truncateTrackMessage(current),
			})
			continue
		}
		track.Status = "active"
		track.Mentions++
		track.LastSeenAt = now
		track.LastMessage = truncateTrackMessage(current)
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].LastSeenAt.After(tracks[j].LastSeenAt)
	})
	if len(tracks) > 25 {
		tracks = tracks[:25]
	}

	saveTopicTracks(workspace, tracks)
}

func loadTopicTracks(workspace string) []TopicTrack {
	path := topicTracksPath(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var tracks []TopicTrack
	if err := json.Unmarshal(data, &tracks); err != nil {
		logger.WarnCF("agent", "Failed to parse topic tracks", map[string]interface{}{"error": err.Error()})
		return nil
	}
	return tracks
}

func saveTopicTracks(workspace string, tracks []TopicTrack) {
	path := topicTracksPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		logger.WarnCF("agent", "Failed to create topic track directory", map[string]interface{}{"error": err.Error()})
		return
	}

	data, err := json.MarshalIndent(tracks, "", "  ")
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal topic tracks", map[string]interface{}{"error": err.Error()})
		return
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logger.WarnCF("agent", "Failed to write topic tracks", map[string]interface{}{"error": err.Error()})
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.WarnCF("agent", "Failed to save topic tracks", map[string]interface{}{"error": err.Error()})
	}
}

func topicTracksPath(workspace string) string {
	return filepath.Join(workspace, "state", "topic_tracks.json")
}

func truncateTrackMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) <= 160 {
		return msg
	}
	return msg[:160] + "..."
}
