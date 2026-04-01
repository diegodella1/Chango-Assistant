package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/telemetry"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type TopicTrack struct {
	Topic           string    `json:"topic"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	Mentions        int       `json:"mentions"`
	Priority        int       `json:"priority"`
	Confidence      float64   `json:"confidence"`
	NextAction      string    `json:"next_action"`
	CooldownUntil   time.Time `json:"cooldown_until,omitempty"`
	LastPromotedAt  time.Time `json:"last_promoted_at,omitempty"`
	KnowledgeStatus string    `json:"knowledge_status,omitempty"`
	TaskStatus      string    `json:"task_status,omitempty"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	LastMessage     string    `json:"last_message,omitempty"`
	Evidence        []string  `json:"evidence,omitempty"`
}

func updateTopicTracks(workspace, current string, history []providers.Message) []TopicTrack {
	topics := detectRecurringTopics(current, history, 3)
	if len(topics) == 0 {
		return loadTopicTracks(workspace)
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
				Evidence:    []string{truncateTrackMessage(current)},
			})
			track = &tracks[len(tracks)-1]
			byTopic[topic] = track
		} else {
			track.Status = "active"
			track.Mentions++
			track.LastSeenAt = now
			track.LastMessage = truncateTrackMessage(current)
			track.Evidence = appendTrackEvidence(track.Evidence, current)
		}

		evaluateTopicTrack(workspace, track)
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].LastSeenAt.After(tracks[j].LastSeenAt)
	})
	if len(tracks) > 25 {
		tracks = tracks[:25]
	}

	saveTopicTracks(workspace, tracks)
	return tracks
}

func evaluateTopicTrack(workspace string, track *TopicTrack) {
	track.Priority = minInt(track.Mentions+1, 5)
	track.Confidence = minFloat(0.3+float64(track.Mentions)*0.15, 0.95)
	track.KnowledgeStatus = knowledgeStatusForTopic(workspace, track.Topic)
	track.TaskStatus = taskStatusForTopic(workspace, track.Topic)

	if !track.CooldownUntil.IsZero() && time.Now().Before(track.CooldownUntil) {
		track.NextAction = "cooldown"
		return
	}

	switch {
	case track.KnowledgeStatus == "ready":
		track.NextAction = "monitor"
	case track.KnowledgeStatus == "researching":
		track.NextAction = "monitor"
	case track.Mentions >= 3 && track.KnowledgeStatus == "":
		track.NextAction = "learn"
	case track.Mentions >= 2 && isActionableTrackMessage(track.LastMessage) && track.TaskStatus == "":
		track.NextAction = "task"
	case track.TaskStatus != "":
		track.NextAction = "monitor"
	default:
		track.NextAction = "watch"
	}
}

func (al *AgentLoop) maybePromoteTopicTracks(tracks []TopicTrack, opts processOptions) {
	if opts.Feature != telemetry.FeatureChat || opts.NoHistory {
		return
	}

	for _, track := range tracks {
		if !track.CooldownUntil.IsZero() && time.Now().Before(track.CooldownUntil) {
			continue
		}
		switch track.NextAction {
		case "learn":
			if al.learnTool == nil {
				continue
			}
			al.learnTool.SetContext(opts.Channel, opts.ChatID)
			result := al.learnTool.Execute(context.Background(), map[string]interface{}{
				"action":  "start",
				"topic":   track.Topic,
				"purpose": "investigación autónoma de tema recurrente",
				"depth":   "overview",
			})
			if result != nil && !result.IsError {
				updateTrackPromotion(al.workspace, track.Topic, "researching", "", 6*time.Hour)
				logger.InfoCF("agent", "Track promoted to learn", map[string]interface{}{
					"topic": track.Topic,
				})
			}
		case "task":
			if createTrackTask(al.workspace, track) {
				updateTrackPromotion(al.workspace, track.Topic, track.KnowledgeStatus, "pending", 6*time.Hour)
				logger.InfoCF("agent", "Track promoted to task", map[string]interface{}{
					"topic": track.Topic,
				})
			}
		}
	}
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

func updateTrackPromotion(workspace, topic, knowledgeStatus, taskStatus string, cooldown time.Duration) {
	tracks := loadTopicTracks(workspace)
	now := time.Now()
	for i := range tracks {
		if tracks[i].Topic != topic {
			continue
		}
		tracks[i].LastPromotedAt = now
		tracks[i].CooldownUntil = now.Add(cooldown)
		if knowledgeStatus != "" {
			tracks[i].KnowledgeStatus = knowledgeStatus
		}
		if taskStatus != "" {
			tracks[i].TaskStatus = taskStatus
		}
		tracks[i].NextAction = "cooldown"
		break
	}
	saveTopicTracks(workspace, tracks)
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

func appendTrackEvidence(existing []string, msg string) []string {
	msg = truncateTrackMessage(msg)
	if msg == "" {
		return existing
	}
	if len(existing) > 0 && existing[len(existing)-1] == msg {
		return existing
	}
	existing = append(existing, msg)
	if len(existing) > 5 {
		existing = existing[len(existing)-5:]
	}
	return existing
}

func knowledgeStatusForTopic(workspace, topic string) string {
	slug := trackSlugify(topic)
	metaPath := filepath.Join(workspace, "knowledge", slug, "META.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.Status
}

func taskStatusForTopic(workspace, topic string) string {
	data, err := os.ReadFile(filepath.Join(workspace, "tasks", "tasks.json"))
	if err != nil {
		return ""
	}

	var tasks []struct {
		Status string   `json:"status"`
		Tags   []string `json:"tags"`
		Title  string   `json:"title"`
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return ""
	}

	tag := topicTaskTag(topic)
	for _, task := range tasks {
		for _, taskTag := range task.Tags {
			if taskTag == tag {
				return task.Status
			}
		}
	}
	return ""
}

func createTrackTask(workspace string, track TopicTrack) bool {
	if taskStatusForTopic(workspace, track.Topic) != "" {
		return false
	}

	taskTool := tools.NewTasksTool(workspace)
	result := taskTool.Execute(context.Background(), map[string]interface{}{
		"action":      "add",
		"title":       "Seguimiento autonomo: " + track.Topic,
		"description": "Tema recurrente promovido automaticamente desde los topic tracks.",
		"priority":    taskPriorityForTrack(track.Priority),
		"tags":        []interface{}{"autonomy-track", topicTaskTag(track.Topic)},
		"notes":       track.LastMessage,
	})
	return result != nil && !result.IsError
}

func topicTaskTag(topic string) string {
	return "topic:" + trackSlugify(topic)
}

func taskPriorityForTrack(priority int) string {
	switch {
	case priority >= 4:
		return "high"
	case priority <= 2:
		return "low"
	default:
		return "medium"
	}
}

func isActionableTrackMessage(msg string) bool {
	lower := strings.ToLower(msg)
	keywords := []string{
		"hacer", "armar", "implementar", "resolver", "seguir", "desplegar",
		"deploy", "mandar", "revisar", "investigar", "prioridad", "objetivo",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func trackSlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"ñ", "n", "ü", "u",
	)
	s = replacer.Replace(s)
	reg := regexp.MustCompile(`[^a-z0-9]+`)
	s = reg.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
