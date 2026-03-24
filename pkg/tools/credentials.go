package tools

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sipeed/picoclaw/pkg/logger"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Credential represents a stored service credential.
type Credential struct {
	Service   string `json:"service"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	Password  string `json:"password"` // AES-256-GCM encrypted, base64 encoded
	Email     string `json:"email"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CredentialStore is the persisted JSON structure.
type CredentialStore struct {
	Version     int          `json:"version"`
	Credentials []Credential `json:"credentials"`
}

// CredentialsTool provides an encrypted credential vault.
type CredentialsTool struct {
	workspace string
	masterKey []byte // 32 bytes for AES-256
	mu        sync.RWMutex
	store     *CredentialStore
	filePath  string
	encrypted bool // false if no master key (plaintext mode)
}

// NewCredentialsTool creates a new credentials tool.
// If masterKeyStr is empty, derives a key from the workspace path (not secure but functional).
func NewCredentialsTool(workspace string, masterKeyStr string) *CredentialsTool {
	if masterKeyStr == "" {
		masterKeyStr = workspace
		logger.WarnC("credentials", "no master key configured, using workspace-derived key (not secure)")
	}

	hash := sha256.Sum256([]byte(masterKeyStr))

	fp := filepath.Join(workspace, "state", "credentials.json")
	t := &CredentialsTool{
		workspace: workspace,
		masterKey: hash[:],
		filePath:  fp,
		encrypted: true,
	}
	t.store = t.loadStore()
	return t
}

func (t *CredentialsTool) Name() string { return "credentials" }

func (t *CredentialsTool) Description() string {
	return "Encrypted credential vault for service accounts. Store, retrieve, list, update, and delete credentials securely."
}

func (t *CredentialsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"store", "get", "list", "delete", "update"},
				"description": "Action to perform",
			},
			"service": map[string]interface{}{
				"type":        "string",
				"description": "Service name (e.g. github, hackernews). Required for store, get, delete, update",
			},
			"username": map[string]interface{}{
				"type":        "string",
				"description": "Username or login handle",
			},
			"password": map[string]interface{}{
				"type":        "string",
				"description": "Password or secret (will be encrypted at rest)",
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "Login URL for the service",
			},
			"email": map[string]interface{}{
				"type":        "string",
				"description": "Email used to register",
			},
			"notes": map[string]interface{}{
				"type":        "string",
				"description": "Any extra information",
			},
		},
		"required": []string{"action"},
	}
}

func (t *CredentialsTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "store":
		return t.store_(args)
	case "get":
		return t.get(args)
	case "list":
		return t.list()
	case "delete":
		return t.delete_(args)
	case "update":
		return t.update(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s. Valid: store, get, list, delete, update", action))
	}
}

// encrypt encrypts plaintext using AES-256-GCM and returns base64(nonce+ciphertext).
func (t *CredentialsTool) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(t.masterKey)
	if err != nil {
		return "", fmt.Errorf("cipher init: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm init: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce gen: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decodes base64, splits nonce, and decrypts AES-256-GCM.
func (t *CredentialsTool) decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(t.masterKey)
	if err != nil {
		return "", fmt.Errorf("cipher init: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm init: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

func (t *CredentialsTool) loadStore() *CredentialStore {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return &CredentialStore{Version: 1, Credentials: []Credential{}}
	}
	var s CredentialStore
	if err := json.Unmarshal(data, &s); err != nil {
		logger.ErrorC("credentials", fmt.Sprintf("failed to parse store: %v", err))
		return &CredentialStore{Version: 1, Credentials: []Credential{}}
	}
	return &s
}

func (t *CredentialsTool) saveStore() error {
	dir := filepath.Dir(t.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	data, err := json.MarshalIndent(t.store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	tmp := t.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, t.filePath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (t *CredentialsTool) findByService(service string) int {
	svc := strings.ToLower(service)
	for i, c := range t.store.Credentials {
		if strings.ToLower(c.Service) == svc {
			return i
		}
	}
	return -1
}

func (t *CredentialsTool) store_(args map[string]interface{}) *ToolResult {
	service, _ := args["service"].(string)
	password, _ := args["password"].(string)
	username, _ := args["username"].(string)
	if service == "" || password == "" {
		return ErrorResult("service and password are required for store")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if idx := t.findByService(service); idx >= 0 {
		return ErrorResult(fmt.Sprintf("credential for '%s' already exists. Use update to modify it", service))
	}

	encPass, err := t.encrypt(password)
	if err != nil {
		return ErrorResult(fmt.Sprintf("encryption failed: %v", err))
	}

	now := time.Now().Format(time.RFC3339)
	cred := Credential{
		Service:   service,
		URL:       strArg(args, "url"),
		Username:  username,
		Password:  encPass,
		Email:     strArg(args, "email"),
		Notes:     strArg(args, "notes"),
		CreatedAt: now,
		UpdatedAt: now,
	}

	t.store.Credentials = append(t.store.Credentials, cred)
	if err := t.saveStore(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	return SilentResult(fmt.Sprintf("Credential for '%s' stored (encrypted)", service))
}

func (t *CredentialsTool) get(args map[string]interface{}) *ToolResult {
	service, _ := args["service"].(string)
	if service == "" {
		return ErrorResult("service is required for get")
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	idx := t.findByService(service)
	if idx < 0 {
		return SilentResult(fmt.Sprintf("No credential found for '%s'", service))
	}

	cred := t.store.Credentials[idx]
	plainPass, err := t.decrypt(cred.Password)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to decrypt password for '%s': %v", service, err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Service: %s\n", cred.Service))
	if cred.URL != "" {
		sb.WriteString(fmt.Sprintf("URL: %s\n", cred.URL))
	}
	if cred.Username != "" {
		sb.WriteString(fmt.Sprintf("Username: %s\n", cred.Username))
	}
	sb.WriteString(fmt.Sprintf("Password: %s\n", plainPass))
	if cred.Email != "" {
		sb.WriteString(fmt.Sprintf("Email: %s\n", cred.Email))
	}
	if cred.Notes != "" {
		sb.WriteString(fmt.Sprintf("Notes: %s\n", cred.Notes))
	}
	sb.WriteString(fmt.Sprintf("Updated: %s", cred.UpdatedAt))

	return SilentResult(sb.String())
}

func (t *CredentialsTool) list() *ToolResult {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.store.Credentials) == 0 {
		return SilentResult("No credentials stored")
	}

	var lines []string
	for _, c := range t.store.Credentials {
		line := fmt.Sprintf("- %s", c.Service)
		if c.Username != "" {
			line += fmt.Sprintf(" (user: %s)", c.Username)
		}
		if c.URL != "" {
			line += fmt.Sprintf(" — %s", c.URL)
		}
		lines = append(lines, line)
	}

	return SilentResult(fmt.Sprintf("%d credential(s):\n%s", len(t.store.Credentials), strings.Join(lines, "\n")))
}

func (t *CredentialsTool) delete_(args map[string]interface{}) *ToolResult {
	service, _ := args["service"].(string)
	if service == "" {
		return ErrorResult("service is required for delete")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	idx := t.findByService(service)
	if idx < 0 {
		return SilentResult(fmt.Sprintf("No credential found for '%s'", service))
	}

	t.store.Credentials = append(t.store.Credentials[:idx], t.store.Credentials[idx+1:]...)
	if err := t.saveStore(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	return SilentResult(fmt.Sprintf("Credential for '%s' deleted", service))
}

func (t *CredentialsTool) update(args map[string]interface{}) *ToolResult {
	service, _ := args["service"].(string)
	if service == "" {
		return ErrorResult("service is required for update")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	idx := t.findByService(service)
	if idx < 0 {
		return ErrorResult(fmt.Sprintf("No credential found for '%s'", service))
	}

	cred := &t.store.Credentials[idx]

	if v, ok := args["username"].(string); ok && v != "" {
		cred.Username = v
	}
	if v, ok := args["url"].(string); ok && v != "" {
		cred.URL = v
	}
	if v, ok := args["email"].(string); ok && v != "" {
		cred.Email = v
	}
	if v, ok := args["notes"].(string); ok && v != "" {
		cred.Notes = v
	}
	if v, ok := args["password"].(string); ok && v != "" {
		encPass, err := t.encrypt(v)
		if err != nil {
			return ErrorResult(fmt.Sprintf("encryption failed: %v", err))
		}
		cred.Password = encPass
	}

	cred.UpdatedAt = time.Now().Format(time.RFC3339)

	if err := t.saveStore(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	return SilentResult(fmt.Sprintf("Credential for '%s' updated", service))
}

// strArg is a helper to extract an optional string arg.
func strArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}
