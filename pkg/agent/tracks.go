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

	"github.com/sipeed/picoclaw/pkg/bus"
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
	RiskLevel       string    `json:"risk_level,omitempty"`
	RequiresApproval bool     `json:"requires_approval,omitempty"`
	DecisionReason  string    `json:"decision_reason,omitempty"`
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
	tracks := reconcileTopicTracks(workspace)
	topics := detectRecurringTopics(current, history, 3)
	if len(topics) == 0 {
		return tracks
	}

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

func reconcileTopicTracks(workspace string) []TopicTrack {
	tracks := loadTopicTracks(workspace)
	if len(tracks) == 0 {
		return nil
	}

	changed := false
	now := time.Now()
	for i := range tracks {
		track := &tracks[i]
		prevStatus := track.Status
		prevAction := track.NextAction
		prevKnowledge := track.KnowledgeStatus
		prevTask := track.TaskStatus

		track.KnowledgeStatus = knowledgeStatusForTopic(workspace, track.Topic)
		track.TaskStatus = taskStatusForTopic(workspace, track.Topic)

		switch {
		case track.TaskStatus == "done":
			track.Status = "resolved"
			track.NextAction = "none"
			if track.CooldownUntil.Before(now.Add(24 * time.Hour)) {
				track.CooldownUntil = now.Add(24 * time.Hour)
			}
		case track.KnowledgeStatus == "ready" && (track.TaskStatus == "" || track.TaskStatus == "cancelled"):
			track.Status = "resolved"
			track.NextAction = "none"
			if track.CooldownUntil.Before(now.Add(24 * time.Hour)) {
				track.CooldownUntil = now.Add(24 * time.Hour)
			}
		case track.TaskStatus == "in_progress" || track.KnowledgeStatus == "researching":
			track.Status = "active"
			track.NextAction = "monitor"
		case track.KnowledgeStatus == "failed":
			track.Status = "active"
			track.NextAction = "watch"
			track.Confidence = minFloat(track.Confidence, 0.45)
		case track.TaskStatus == "cancelled":
			track.Status = "active"
			track.NextAction = "watch"
		default:
			evaluateTopicTrack(workspace, track)
		}

		if prevStatus != track.Status || prevAction != track.NextAction || prevKnowledge != track.KnowledgeStatus || prevTask != track.TaskStatus {
			changed = true
		}
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].LastSeenAt.After(tracks[j].LastSeenAt)
	})
	if changed {
		saveTopicTracks(workspace, tracks)
	}
	return tracks
}

func evaluateTopicTrack(workspace string, track *TopicTrack) {
	track.Priority = minInt(track.Mentions+1, 5)
	track.Confidence = minFloat(0.3+float64(track.Mentions)*0.15, 0.95)
	track.KnowledgeStatus = knowledgeStatusForTopic(workspace, track.Topic)
	track.TaskStatus = taskStatusForTopic(workspace, track.Topic)
	track.RiskLevel = classifyTrackRisk(track.LastMessage)
	track.RequiresApproval = track.RiskLevel == "high"

	if !track.CooldownUntil.IsZero() && time.Now().Before(track.CooldownUntil) {
		track.NextAction = "cooldown"
		track.DecisionReason = "track in cooldown"
		return
	}

	action, reason := decideTrackPolicy(track)
	track.NextAction = action
	track.DecisionReason = reason
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
		case "ask_user":
			if opts.Channel == "" || opts.ChatID == "" {
				continue
			}
			al.bus.PublishOutbound(bus.OutboundMessage{
				Channel: opts.Channel,
				ChatID:  opts.ChatID,
				Content: buildApprovalRequest(track),
			})
			updateTrackPromotion(al.workspace, track.Topic, track.KnowledgeStatus, track.TaskStatus, 12*time.Hour)
			logger.InfoCF("agent", "Track promoted to approval request", map[string]interface{}{
				"topic": track.Topic,
				"risk":  track.RiskLevel,
			})
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

func classifyTrackRisk(msg string) string {
	lower := strings.ToLower(msg)
	highRisk := []string{
		"producción", "produccion", "prod", "deploy", "desplegar",
		"pagar", "wallet", "dinero", "transfer", "invoice",
		"email", "mail", "gmail", "externo", "cliente", "usuario",
		"borrar", "delete", "eliminar", "credenciales", "password",
	}
	mediumRisk := []string{
		"config", "settings", "automatizar", "cron", "recordatorio",
		"seguimiento", "task", "agenda", "calendar", "archivo",
	}

	for _, token := range highRisk {
		if strings.Contains(lower, token) {
			return "high"
		}
	}
	for _, token := range mediumRisk {
		if strings.Contains(lower, token) {
			return "medium"
		}
	}
	return "low"
}

func decideTrackPolicy(track *TopicTrack) (string, string) {
	switch {
	case track.TaskStatus == "done":
		return "none", "task already completed"
	case track.KnowledgeStatus == "ready" && (track.TaskStatus == "" || track.TaskStatus == "cancelled"):
		return "none", "knowledge already available"
	case track.TaskStatus == "in_progress" || track.KnowledgeStatus == "researching":
		return "monitor", "work already in progress"
	case track.RequiresApproval && isActionableTrackMessage(track.LastMessage):
		return "ask_user", "high-risk action requires approval"
	case track.Mentions >= 3 && track.KnowledgeStatus == "":
		return "learn", "recurrent topic without knowledge"
	case track.Mentions >= 2 && isActionableTrackMessage(track.LastMessage) && track.TaskStatus == "":
		return "task", "actionable repeated topic without follow-up task"
	case track.Mentions <= 1 && track.Confidence < 0.5:
		return "ignore", "weak signal"
	default:
		return "watch", "keep monitoring"
	}
}

func buildApprovalRequest(track TopicTrack) string {
	return "Quiero avanzar con un track sensible: " + track.Topic + ". " +
		"Riesgo: " + track.RiskLevel + ". " +
		"Razón: " + track.DecisionReason + ". " +
		"¿Procedo o preferís que solo lo siga observando?"
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
