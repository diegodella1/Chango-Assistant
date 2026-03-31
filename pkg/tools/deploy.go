package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// appUUIDs maps known app names to their Coolify UUIDs.
var appUUIDs = map[string]string{
	"picoclaw": "vk4goko0koc8k4c48sckwsk8",
}

var appHealthURLs = map[string]string{
	"picoclaw": "http://127.0.0.1:18790/health",
}

type DeploySendCallback func(channel, chatID, content string) error

// DeployTool manages Coolify deployments via API.
type DeployTool struct {
	baseURL        string
	httpClient     *http.Client
	defaultChannel string
	defaultChatID  string
	sendCallback   DeploySendCallback
}

func NewDeployTool() *DeployTool {
	return &DeployTool{
		baseURL: "http://127.0.0.1:8000",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *DeployTool) Name() string { return "deploy" }

func (t *DeployTool) SetContext(channel, chatID string) {
	t.defaultChannel = channel
	t.defaultChatID = chatID
}

func (t *DeployTool) SetSendCallback(callback DeploySendCallback) {
	t.sendCallback = callback
}

func (t *DeployTool) Description() string {
	return "Coolify deployment operations. Actions: deploy (restart an app), status (check deployment status), logs (get deployment logs). " +
		"Known apps: picoclaw. You can also pass a UUID directly."
}

func (t *DeployTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"deploy", "status", "logs"},
				"description": "Action to perform",
			},
			"app": map[string]interface{}{
				"type":        "string",
				"description": "App name (e.g. picoclaw) or Coolify app UUID (for deploy action)",
			},
			"deployment_uuid": map[string]interface{}{
				"type":        "string",
				"description": "Deployment UUID (for status and logs actions)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *DeployTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "deploy":
		return t.deploy(ctx, args)
	case "status":
		return t.status(ctx, args)
	case "logs":
		return t.logs(ctx, args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *DeployTool) deploy(ctx context.Context, args map[string]interface{}) *ToolResult {
	app, _ := args["app"].(string)
	if app == "" {
		return ErrorResult("app is required for deploy action")
	}

	// Resolve app name to UUID
	uuid := t.resolveAppUUID(app)
	if uuid == "" {
		return ErrorResult(fmt.Sprintf("unknown app: %s (known apps: %s)", app, strings.Join(knownAppNames(), ", ")))
	}

	// Create temp token
	tokenID, bearer, err := t.createTempToken(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create API token: %s", err))
	}
	defer t.cleanupToken(ctx, tokenID)

	// POST restart
	url := fmt.Sprintf("%s/api/v1/applications/%s/restart", t.baseURL, uuid)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %s", err))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("deploy request failed: %s", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return ErrorResult(fmt.Sprintf("deploy failed (HTTP %d): %s", resp.StatusCode, string(body)))
	}

	// Extract deployment_uuid from response
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return SilentResult(fmt.Sprintf("Deploy triggered for %s. Response: %s", app, string(body)))
	}

	deployUUID, _ := result["deployment_uuid"].(string)
	if deployUUID == "" {
		// Try nested
		if msg, ok := result["message"].(string); ok {
			return SilentResult(fmt.Sprintf("Deploy triggered for %s: %s\nFull response: %s", app, msg, string(body)))
		}
		return SilentResult(fmt.Sprintf("Deploy triggered for %s. Response: %s", app, string(body)))
	}

	t.startFailureMonitor(app, uuid, deployUUID)

	return SilentResult(fmt.Sprintf("Deploy triggered for %s. deployment_uuid: %s\nUse deploy status to check progress.", app, deployUUID))
}

func (t *DeployTool) status(ctx context.Context, args map[string]interface{}) *ToolResult {
	deployUUID, _ := args["deployment_uuid"].(string)
	if deployUUID == "" {
		return ErrorResult("deployment_uuid is required for status action")
	}

	tokenID, bearer, err := t.createTempToken(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create API token: %s", err))
	}
	defer t.cleanupToken(ctx, tokenID)

	url := fmt.Sprintf("%s/api/v1/deployments/%s", t.baseURL, deployUUID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %s", err))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("status request failed: %s", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return ErrorResult(fmt.Sprintf("status check failed (HTTP %d): %s", resp.StatusCode, string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return SilentResult(string(body))
	}

	status, _ := result["status"].(string)
	return SilentResult(fmt.Sprintf("Deployment %s status: %s", deployUUID, status))
}

func (t *DeployTool) logs(ctx context.Context, args map[string]interface{}) *ToolResult {
	deployUUID, _ := args["deployment_uuid"].(string)
	if deployUUID == "" {
		return ErrorResult("deployment_uuid is required for logs action")
	}

	tokenID, bearer, err := t.createTempToken(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create API token: %s", err))
	}
	defer t.cleanupToken(ctx, tokenID)

	url := fmt.Sprintf("%s/api/v1/deployments/%s", t.baseURL, deployUUID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %s", err))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("logs request failed: %s", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return ErrorResult(fmt.Sprintf("logs fetch failed (HTTP %d): %s", resp.StatusCode, string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return SilentResult(string(body))
	}

	logs, _ := result["logs"].(string)
	status, _ := result["status"].(string)
	if logs == "" {
		logs = "(no logs available)"
	}

	// Truncate if too long
	maxLen := 8000
	if len(logs) > maxLen {
		logs = logs[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(logs)-maxLen)
	}

	return SilentResult(fmt.Sprintf("Deployment %s [%s] logs:\n%s", deployUUID, status, logs))
}

// resolveAppUUID resolves an app name or UUID to a Coolify app UUID.
func (t *DeployTool) resolveAppUUID(app string) string {
	// Check if it's already a UUID (contains no spaces and has the right length)
	if len(app) >= 20 && !strings.Contains(app, " ") {
		return app
	}
	// Look up by name
	if uuid, ok := appUUIDs[strings.ToLower(app)]; ok {
		return uuid
	}
	return ""
}

// createTempToken creates a temporary Coolify API token via psql.
// Returns (token_id, bearer_string, error).
func (t *DeployTool) createTempToken(ctx context.Context) (string, string, error) {
	// Generate random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random token: %w", err)
	}
	plainToken := hex.EncodeToString(tokenBytes)

	// SHA256 hash for DB storage
	hash := sha256.Sum256([]byte(plainToken))
	tokenHash := hex.EncodeToString(hash[:])

	// SQL to insert token
	sql := fmt.Sprintf(
		`INSERT INTO personal_access_tokens (tokenable_type, tokenable_id, name, token, abilities, team_id, created_at, updated_at) `+
			`VALUES ('App\Models\User', 0, 'agent-tmp', '%s', '["*"]', 0, now(), now()) RETURNING id;`,
		tokenHash,
	)

	out, err := t.runDockerExec(ctx, "coolify-db", "psql", "-U", "coolify", "-d", "coolify", "-t", "-A", "-c", sql)
	if err != nil {
		return "", "", fmt.Errorf("psql insert failed: %w (output: %s)", err, out)
	}

	// Extract token ID from output (first line, numeric)
	tokenID := strings.TrimSpace(strings.Split(out, "\n")[0])
	if tokenID == "" {
		return "", "", fmt.Errorf("failed to extract token ID from psql output: %s", out)
	}

	bearer := fmt.Sprintf("%s|%s", tokenID, plainToken)
	return tokenID, bearer, nil
}

// cleanupToken removes a temporary API token from the DB.
func (t *DeployTool) cleanupToken(ctx context.Context, tokenID string) {
	sql := fmt.Sprintf("DELETE FROM personal_access_tokens WHERE id = %s;", tokenID)
	_, _ = t.runDockerExec(ctx, "coolify-db", "psql", "-U", "coolify", "-d", "coolify", "-c", sql)
}

// runDockerExec runs a command inside a Docker container, using nsenter if inside a container.
func (t *DeployTool) runDockerExec(ctx context.Context, container string, cmdArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Build docker exec command
	dockerArgs := append([]string{"exec", container}, cmdArgs...)

	var cmd *exec.Cmd
	if isInsideContainer() {
		// Use nsenter to reach host Docker
		nsenterArgs := []string{
			"--mount=/hostfs/proc/1/ns/mnt",
			"--uts=/hostfs/proc/1/ns/uts",
			"--ipc=/hostfs/proc/1/ns/ipc",
			"--net=/hostfs/proc/1/ns/net",
			"--root=/hostfs",
			"--", "docker",
		}
		nsenterArgs = append(nsenterArgs, dockerArgs...)
		cmd = exec.CommandContext(ctx, "nsenter", nsenterArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "docker", dockerArgs...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s", errMsg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// isInsideContainer checks if the process is running inside a container
// by looking for /hostfs (the host filesystem mount).
func isInsideContainer() bool {
	_, err := os.Stat("/hostfs")
	return err == nil
}

// knownAppNames returns the list of known app names.
func knownAppNames() []string {
	names := make([]string, 0, len(appUUIDs))
	for name := range appUUIDs {
		names = append(names, name)
	}
	return names
}

func (t *DeployTool) startFailureMonitor(app, uuid, deployUUID string) {
	if t.sendCallback == nil || t.defaultChannel == "" || t.defaultChatID == "" || deployUUID == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				t.notifyFailure(fmt.Sprintf("⚠️ Deploy %s sin estado final tras 12m (deployment_uuid: %s). Revisá Coolify.", app, deployUUID))
				return
			case <-time.After(10 * time.Second):
				status, logs, err := t.fetchDeploymentSnapshot(ctx, deployUUID)
				if err != nil {
					logger.WarnCF("deploy", "Failed to poll deployment status", map[string]interface{}{
						"deployment_uuid": deployUUID,
						"error":           err.Error(),
					})
					continue
				}
				if status == "" || status == "in_progress" || status == "queued" || status == "running" {
					continue
				}
				if isFailedDeployStatus(status) {
					t.notifyFailure(fmt.Sprintf("❌ Deploy falló (%s) para %s (%s). deployment_uuid: %s\n%s", status, app, uuid, deployUUID, summarizeLogs(logs)))
					return
				}
				if status == "finished" {
					if err := t.checkHealth(ctx, app, uuid); err != nil {
						t.notifyFailure(fmt.Sprintf("⚠️ Deploy terminó pero health-check falló para %s (%s): %v\ndeployment_uuid: %s", app, uuid, err, deployUUID))
					}
					return
				}
			}
		}
	}()
}

func (t *DeployTool) fetchDeploymentSnapshot(ctx context.Context, deployUUID string) (string, string, error) {
	tokenID, bearer, err := t.createTempToken(ctx)
	if err != nil {
		return "", "", fmt.Errorf("create token: %w", err)
	}
	defer t.cleanupToken(ctx, tokenID)

	url := fmt.Sprintf("%s/api/v1/deployments/%s", t.baseURL, deployUUID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", err
	}

	status, _ := result["status"].(string)
	logs, _ := result["logs"].(string)
	return strings.ToLower(strings.TrimSpace(status)), logs, nil
}

func (t *DeployTool) checkHealth(ctx context.Context, app, uuid string) error {
	url := resolveHealthURL(app, uuid)
	if url == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (t *DeployTool) notifyFailure(content string) {
	if t.sendCallback == nil || t.defaultChannel == "" || t.defaultChatID == "" || strings.TrimSpace(content) == "" {
		return
	}
	if err := t.sendCallback(t.defaultChannel, t.defaultChatID, content); err != nil {
		logger.ErrorCF("deploy", "Failed to send deploy failure notification", map[string]interface{}{
			"channel": t.defaultChannel,
			"chat_id": t.defaultChatID,
			"error":   err.Error(),
		})
	}
}

func isFailedDeployStatus(status string) bool {
	switch status {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func resolveHealthURL(app, uuid string) string {
	if v, ok := appHealthURLs[strings.ToLower(strings.TrimSpace(app))]; ok {
		return v
	}
	for name, id := range appUUIDs {
		if strings.EqualFold(id, uuid) {
			return appHealthURLs[name]
		}
	}
	return ""
}

func summarizeLogs(logs string) string {
	logs = strings.TrimSpace(logs)
	if logs == "" {
		return "No logs disponibles."
	}
	const max = 700
	if len(logs) <= max {
		return logs
	}
	return "Últimas líneas:\n" + logs[len(logs)-max:]
}
