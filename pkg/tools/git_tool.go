package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitTool provides git operations for repository management.
type GitTool struct {
	repoDir string
}

func NewGitTool(repoDir string) *GitTool {
	return &GitTool{repoDir: repoDir}
}

func (t *GitTool) Name() string { return "git" }

func (t *GitTool) Description() string {
	return "Git operations on the picoclaw repository. Actions: status (working tree status), diff (show changes), " +
		"log (recent commits), commit (stage files and commit), push (push to remote), branch (list branches), " +
		"checkout (switch branch)."
}

func (t *GitTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"status", "diff", "log", "commit", "push", "branch", "checkout"},
				"description": "Git action to perform",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Commit message (for commit action)",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Files to stage (for commit action)",
			},
			"staged": map[string]interface{}{
				"type":        "boolean",
				"description": "Show staged changes only (for diff action, uses --cached)",
			},
			"count": map[string]interface{}{
				"type":        "number",
				"description": "Number of log entries to show (default 10, for log action)",
			},
			"remote": map[string]interface{}{
				"type":        "string",
				"description": "Remote name for push (default: fork)",
			},
			"branch": map[string]interface{}{
				"type":        "string",
				"description": "Branch name (for push or checkout action, default: main for push)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GitTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "status":
		return t.gitStatus(ctx)
	case "diff":
		return t.gitDiff(ctx, args)
	case "log":
		return t.gitLog(ctx, args)
	case "commit":
		return t.gitCommit(ctx, args)
	case "push":
		return t.gitPush(ctx, args)
	case "branch":
		return t.gitBranch(ctx)
	case "checkout":
		return t.gitCheckout(ctx, args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *GitTool) gitStatus(ctx context.Context) *ToolResult {
	out, err := t.runGit(ctx, "status", "--porcelain")
	if err != nil {
		return ErrorResult(err.Error())
	}
	if out == "" {
		out = "(working tree clean)"
	}
	return SilentResult(out)
}

func (t *GitTool) gitDiff(ctx context.Context, args map[string]interface{}) *ToolResult {
	gitArgs := []string{"diff"}
	if staged, ok := args["staged"].(bool); ok && staged {
		gitArgs = append(gitArgs, "--cached")
	}
	out, err := t.runGit(ctx, gitArgs...)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if out == "" {
		out = "(no changes)"
	}
	// Truncate large diffs
	maxLen := 10000
	if len(out) > maxLen {
		out = out[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(out)-maxLen)
	}
	return SilentResult(out)
}

func (t *GitTool) gitLog(ctx context.Context, args map[string]interface{}) *ToolResult {
	count := intArg(args, "count", 10)
	out, err := t.runGit(ctx, "log", "--oneline", fmt.Sprintf("-%d", count))
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GitTool) gitCommit(ctx context.Context, args map[string]interface{}) *ToolResult {
	message, _ := args["message"].(string)
	if message == "" {
		return ErrorResult("message is required for commit")
	}

	// Extract files list
	filesRaw, ok := args["files"]
	if !ok {
		return ErrorResult("files is required for commit")
	}

	var files []string
	switch v := filesRaw.(type) {
	case []interface{}:
		for _, f := range v {
			if s, ok := f.(string); ok && s != "" {
				files = append(files, s)
			}
		}
	case []string:
		files = v
	}

	if len(files) == 0 {
		return ErrorResult("files list cannot be empty for commit")
	}

	// Safety: refuse to commit sensitive files
	for _, f := range files {
		lower := strings.ToLower(f)
		if strings.Contains(lower, ".env") || strings.Contains(lower, "credentials") || strings.Contains(lower, "secret") {
			return ErrorResult(fmt.Sprintf("refusing to commit potentially sensitive file: %s", f))
		}
	}

	// Stage files
	addArgs := append([]string{"add"}, files...)
	if _, err := t.runGit(ctx, addArgs...); err != nil {
		return ErrorResult(fmt.Sprintf("git add failed: %s", err))
	}

	// Commit
	out, err := t.runGit(ctx, "commit", "-m", message)
	if err != nil {
		return ErrorResult(fmt.Sprintf("git commit failed: %s", err))
	}

	return SilentResult(fmt.Sprintf("Committed successfully:\n%s", out))
}

func (t *GitTool) gitPush(ctx context.Context, args map[string]interface{}) *ToolResult {
	remote, _ := args["remote"].(string)
	if remote == "" {
		remote = "fork"
	}
	branch, _ := args["branch"].(string)
	if branch == "" {
		branch = "main"
	}

	// Safety: block force push
	if strings.Contains(remote, "--force") || strings.Contains(branch, "--force") ||
		strings.Contains(remote, "-f") || strings.Contains(branch, "-f") {
		return ErrorResult("force push is not allowed")
	}

	out, err := t.runGit(ctx, "push", remote, branch)
	if err != nil {
		return ErrorResult(fmt.Sprintf("git push failed: %s", err))
	}
	if out == "" {
		out = "(push completed)"
	}
	return SilentResult(out)
}

func (t *GitTool) gitBranch(ctx context.Context) *ToolResult {
	out, err := t.runGit(ctx, "branch")
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GitTool) gitCheckout(ctx context.Context, args map[string]interface{}) *ToolResult {
	branch, _ := args["branch"].(string)
	if branch == "" {
		return ErrorResult("branch is required for checkout")
	}

	// Safety: block dangerous flags
	if strings.HasPrefix(branch, "-") {
		return ErrorResult("branch name cannot start with a dash")
	}

	out, err := t.runGit(ctx, "checkout", branch)
	if err != nil {
		return ErrorResult(fmt.Sprintf("git checkout failed: %s", err))
	}
	if out == "" {
		out = fmt.Sprintf("Switched to branch '%s'", branch)
	}
	return SilentResult(out)
}

// runGit executes a git command in the repo directory with a 30-second timeout.
func (t *GitTool) runGit(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if gitIsInsideContainer() {
		// Use nsenter to reach host git
		nsenterArgs := []string{
			"--mount=/hostfs/proc/1/ns/mnt",
			"--uts=/hostfs/proc/1/ns/uts",
			"--ipc=/hostfs/proc/1/ns/ipc",
			"--net=/hostfs/proc/1/ns/net",
			"--root=/hostfs",
			"--", "git",
		}
		// Add -C for repo dir (since we can't use cmd.Dir across namespaces)
		nsenterArgs = append(nsenterArgs, "-C", t.repoDir)
		nsenterArgs = append(nsenterArgs, args...)
		cmd = exec.CommandContext(ctx, "nsenter", nsenterArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "git", args...)
		cmd.Dir = t.repoDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], errMsg)
	}

	result := strings.TrimSpace(stdout.String())
	// git push writes to stderr normally, include it
	if result == "" && stderr.Len() > 0 {
		result = strings.TrimSpace(stderr.String())
	}

	return result, nil
}

// gitIsInsideContainer checks if running inside a container via /hostfs.
func gitIsInsideContainer() bool {
	_, err := os.Stat("/hostfs")
	return err == nil
}
