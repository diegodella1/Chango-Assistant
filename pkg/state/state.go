package state

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sipeed/picoclaw/pkg/logger"
	"path/filepath"
	"sync"
	"time"
)

// State represents the persistent state for a workspace.
// It includes information about the last active channel/chat.
type State struct {
	// LastChannel is the last channel used for communication
	LastChannel string `json:"last_channel,omitempty"`

	// LastChatID is the last chat ID used for communication
	LastChatID string `json:"last_chat_id,omitempty"`

	// Timestamp is the last time this state was updated
	Timestamp time.Time `json:"timestamp"`

	// WelcomedUsers tracks which users have received the welcome message
	WelcomedUsers map[string]bool `json:"welcomed_users,omitempty"`

	// Structured operational state.
	Tasks       []TaskRecord        `json:"tasks,omitempty"`
	Projects    []ProjectRecord     `json:"projects,omitempty"`
	Blockers    []BlockerRecord     `json:"blockers,omitempty"`
	Reminders   []ReminderRecord    `json:"reminders,omitempty"`
	Decisions   []DecisionRecord    `json:"decisions,omitempty"`
	Commitments []CommitmentRecord  `json:"commitments,omitempty"`
	FollowUps   []FollowUpRecord    `json:"follow_ups,omitempty"`
	AutonomyLog []AutonomyLogRecord `json:"autonomy_log,omitempty"`
}

type TaskRecord struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority,omitempty"`
	DueDate     string   `json:"due_date,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	GoalID      string   `json:"goal_id,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type ProjectRecord struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status,omitempty"`
	CurrentGoal string   `json:"current_goal,omitempty"`
	NextAction  string   `json:"next_action,omitempty"`
	RiskFlags   []string `json:"risk_flags,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

type BlockerRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type ReminderRecord struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	DueAt     string `json:"due_at"`
	Channel   string `json:"channel,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	CreatedAt string `json:"created_at"`
	Fired     bool   `json:"fired"`
}

type DecisionRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Decision  string `json:"decision"`
	Context   string `json:"context,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type CommitmentRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	DueDate     string `json:"due_date,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type FollowUpRecord struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	NextCheckAt   string `json:"next_check_at,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Status        string `json:"status,omitempty"`
	ReferenceID   string `json:"reference_id,omitempty"`
	AttemptCount  int    `json:"attempt_count,omitempty"`
	LastCheckedAt string `json:"last_checked_at,omitempty"`
	LastOutcome   string `json:"last_outcome,omitempty"`
	EscalatedAt   string `json:"escalated_at,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type AutonomyLogRecord struct {
	ID               string `json:"id"`
	Timestamp        string `json:"timestamp"`
	Tool             string `json:"tool,omitempty"`
	Risk             string `json:"risk,omitempty"`
	Status           string `json:"status,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Reason           string `json:"reason,omitempty"`
	ReferenceID      string `json:"reference_id,omitempty"`
	Channel          string `json:"channel,omitempty"`
	ChatID           string `json:"chat_id,omitempty"`
	Approved         bool   `json:"approved,omitempty"`
	Async            bool   `json:"async,omitempty"`
	DurationMs       int64  `json:"duration_ms,omitempty"`
	Verification     string `json:"verification,omitempty"`
	Error            string `json:"error,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

// Manager manages persistent state with atomic saves.
type Manager struct {
	workspace string
	state     *State
	mu        sync.RWMutex
	stateFile string
}

// NewManager creates a new state manager for the given workspace.
func NewManager(workspace string) *Manager {
	stateDir := filepath.Join(workspace, "state")
	stateFile := filepath.Join(stateDir, "state.json")
	oldStateFile := filepath.Join(workspace, "state.json")

	// Create state directory if it doesn't exist
	os.MkdirAll(stateDir, 0755)

	sm := &Manager{
		workspace: workspace,
		stateFile: stateFile,
		state:     &State{},
	}

	// Try to load from new location first
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		// New file doesn't exist, try migrating from old location
		if data, err := os.ReadFile(oldStateFile); err == nil {
			if err := json.Unmarshal(data, sm.state); err == nil {
				// Migrate to new location
				sm.saveAtomic()
				logger.InfoC("state", fmt.Sprintf("migrated state from %s to %s", oldStateFile, stateFile))
			}
		}
	} else {
		// Load from new location
		sm.load()
	}

	return sm
}

// SetLastChannel atomically updates the last channel and saves the state.
// This method uses a temp file + rename pattern for atomic writes,
// ensuring that the state file is never corrupted even if the process crashes.
func (sm *Manager) SetLastChannel(channel string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update state
	sm.state.LastChannel = channel
	sm.state.Timestamp = time.Now()

	// Atomic save using temp file + rename
	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}

	return nil
}

// SetLastChatID atomically updates the last chat ID and saves the state.
func (sm *Manager) SetLastChatID(chatID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update state
	sm.state.LastChatID = chatID
	sm.state.Timestamp = time.Now()

	// Atomic save using temp file + rename
	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}

	return nil
}

// GetLastChannel returns the last channel from the state.
func (sm *Manager) GetLastChannel() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.LastChannel
}

// GetLastChatID returns the last chat ID from the state.
func (sm *Manager) GetLastChatID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.LastChatID
}

// GetTimestamp returns the timestamp of the last state update.
func (sm *Manager) GetTimestamp() time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.Timestamp
}

func (sm *Manager) GetTasks() []TaskRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]TaskRecord, len(sm.state.Tasks))
	copy(result, sm.state.Tasks)
	return result
}

func (sm *Manager) UpsertTask(task TaskRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	replaced := false
	for i := range sm.state.Tasks {
		if sm.state.Tasks[i].ID == task.ID {
			sm.state.Tasks[i] = task
			replaced = true
			break
		}
	}
	if !replaced {
		sm.state.Tasks = append(sm.state.Tasks, task)
	}
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) ReplaceTasks(tasks []TaskRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.Tasks = append([]TaskRecord(nil), tasks...)
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) DeleteTask(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	filtered := sm.state.Tasks[:0]
	for _, task := range sm.state.Tasks {
		if task.ID == id {
			continue
		}
		filtered = append(filtered, task)
	}
	sm.state.Tasks = filtered
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) GetReminders() []ReminderRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]ReminderRecord, len(sm.state.Reminders))
	copy(result, sm.state.Reminders)
	return result
}

func (sm *Manager) ReplaceReminders(reminders []ReminderRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.Reminders = append([]ReminderRecord(nil), reminders...)
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) GetFollowUps() []FollowUpRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]FollowUpRecord, len(sm.state.FollowUps))
	copy(result, sm.state.FollowUps)
	return result
}

func (sm *Manager) UpsertFollowUp(followUp FollowUpRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	replaced := false
	for i := range sm.state.FollowUps {
		if sm.state.FollowUps[i].ID == followUp.ID {
			sm.state.FollowUps[i] = followUp
			replaced = true
			break
		}
	}
	if !replaced {
		sm.state.FollowUps = append(sm.state.FollowUps, followUp)
	}
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) ReplaceFollowUps(followUps []FollowUpRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.FollowUps = append([]FollowUpRecord(nil), followUps...)
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

func (sm *Manager) GetAutonomyLog() []AutonomyLogRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]AutonomyLogRecord, len(sm.state.AutonomyLog))
	copy(result, sm.state.AutonomyLog)
	return result
}

func (sm *Manager) AppendAutonomyLog(entry AutonomyLogRecord) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.AutonomyLog = append(sm.state.AutonomyLog, entry)
	if len(sm.state.AutonomyLog) > 250 {
		sm.state.AutonomyLog = sm.state.AutonomyLog[len(sm.state.AutonomyLog)-250:]
	}
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

// saveAtomic performs an atomic save using temp file + rename.
// This ensures that the state file is never corrupted:
// 1. Write to a temp file
// 2. Rename temp file to target (atomic on POSIX systems)
// 3. If rename fails, cleanup the temp file
//
// Must be called with the lock held.
func (sm *Manager) saveAtomic() error {
	// Create temp file in the same directory as the target
	tempFile := sm.stateFile + ".tmp"

	// Marshal state to JSON
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to temp file
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Atomic rename from temp to target
	if err := os.Rename(tempFile, sm.stateFile); err != nil {
		// Cleanup temp file if rename fails
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// HasBeenWelcomed returns true if the user has already received the welcome message.
func (sm *Manager) HasBeenWelcomed(userID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.state.WelcomedUsers == nil {
		return false
	}
	return sm.state.WelcomedUsers[userID]
}

// MarkWelcomed records that a user has received the welcome message.
func (sm *Manager) MarkWelcomed(userID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.WelcomedUsers == nil {
		sm.state.WelcomedUsers = make(map[string]bool)
	}
	sm.state.WelcomedUsers[userID] = true
	sm.state.Timestamp = time.Now()
	return sm.saveAtomic()
}

// load loads the state from disk.
func (sm *Manager) load() error {
	data, err := os.ReadFile(sm.stateFile)
	if err != nil {
		// File doesn't exist yet, that's OK
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, sm.state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return nil
}
