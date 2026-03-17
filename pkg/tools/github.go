package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GithubTool wraps the gh CLI for GitHub operations.
type GithubTool struct{}

func NewGithubTool() *GithubTool {
	return &GithubTool{}
}

func (t *GithubTool) Name() string { return "github" }

func (t *GithubTool) Description() string {
	return "GitHub operations via gh CLI. Actions: repos (list your repos), issues (list issues), create_issue, pr_list, create_pr, pr_review (view PR details), repo_info. All actions accept 'repo' param as owner/name (e.g. 'diegodella1/picoclaw')."
}

func (t *GithubTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"repos", "issues", "create_issue", "pr_list", "create_pr", "pr_review", "repo_info"},
				"description": "Action to perform",
			},
			"repo": map[string]interface{}{
				"type":        "string",
				"description": "Repository in owner/name format (required for most actions except repos)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Title for create_issue or create_pr",
			},
			"body": map[string]interface{}{
				"type":        "string",
				"description": "Body text for create_issue or create_pr",
			},
			"state": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"open", "closed", "all"},
				"description": "Filter by state (for issues, pr_list). Default: open",
			},
			"base": map[string]interface{}{
				"type":        "string",
				"description": "Base branch for create_pr (default: main)",
			},
			"head": map[string]interface{}{
				"type":        "string",
				"description": "Head branch for create_pr",
			},
			"number": map[string]interface{}{
				"type":        "number",
				"description": "Issue or PR number (for pr_review)",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Max results to return (default: 10)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GithubTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "repos":
		return t.repos(ctx, args)
	case "issues":
		return t.issues(ctx, args)
	case "create_issue":
		return t.createIssue(ctx, args)
	case "pr_list":
		return t.prList(ctx, args)
	case "create_pr":
		return t.createPR(ctx, args)
	case "pr_review":
		return t.prReview(ctx, args)
	case "repo_info":
		return t.repoInfo(ctx, args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *GithubTool) repos(ctx context.Context, args map[string]interface{}) *ToolResult {
	limit := intArg(args, "limit", 10)
	out, err := runGH(ctx, "repo", "list",
		"--json", "nameWithOwner,description,isPrivate,updatedAt,stargazerCount",
		"--limit", fmt.Sprintf("%d", limit))
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) issues(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	if repo == "" {
		return ErrorResult("repo is required for issues")
	}
	st, _ := args["state"].(string)
	if st == "" {
		st = "open"
	}
	limit := intArg(args, "limit", 10)
	out, err := runGH(ctx, "issue", "list",
		"--repo", repo,
		"--state", st,
		"--json", "number,title,state,author,createdAt,labels",
		"--limit", fmt.Sprintf("%d", limit))
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) createIssue(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	if repo == "" || title == "" {
		return ErrorResult("repo and title are required for create_issue")
	}
	ghArgs := []string{"issue", "create", "--repo", repo, "--title", title}
	if body != "" {
		ghArgs = append(ghArgs, "--body", body)
	}
	out, err := runGH(ctx, ghArgs...)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) prList(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	if repo == "" {
		return ErrorResult("repo is required for pr_list")
	}
	st, _ := args["state"].(string)
	if st == "" {
		st = "open"
	}
	limit := intArg(args, "limit", 10)
	out, err := runGH(ctx, "pr", "list",
		"--repo", repo,
		"--state", st,
		"--json", "number,title,state,author,createdAt,headRefName,baseRefName",
		"--limit", fmt.Sprintf("%d", limit))
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) createPR(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	title, _ := args["title"].(string)
	head, _ := args["head"].(string)
	if repo == "" || title == "" || head == "" {
		return ErrorResult("repo, title, and head are required for create_pr")
	}
	base, _ := args["base"].(string)
	if base == "" {
		base = "main"
	}
	body, _ := args["body"].(string)
	ghArgs := []string{"pr", "create", "--repo", repo, "--title", title, "--base", base, "--head", head}
	if body != "" {
		ghArgs = append(ghArgs, "--body", body)
	}
	out, err := runGH(ctx, ghArgs...)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) prReview(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	num := intArg(args, "number", 0)
	if repo == "" || num == 0 {
		return ErrorResult("repo and number are required for pr_review")
	}
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", num),
		"--repo", repo,
		"--json", "number,title,state,body,author,createdAt,headRefName,baseRefName,additions,deletions,commits,reviews,comments")
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

func (t *GithubTool) repoInfo(ctx context.Context, args map[string]interface{}) *ToolResult {
	repo, _ := args["repo"].(string)
	if repo == "" {
		return ErrorResult("repo is required for repo_info")
	}
	out, err := runGH(ctx, "repo", "view", repo,
		"--json", "nameWithOwner,description,isPrivate,defaultBranchRef,stargazerCount,forkCount,issues,pullRequests,createdAt,updatedAt")
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(out)
}

// runGH executes a gh CLI command with a 30-second timeout.
func runGH(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("gh %s: %s", args[0], errMsg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// intArg extracts an integer argument with a default value.
func intArg(args map[string]interface{}, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}
