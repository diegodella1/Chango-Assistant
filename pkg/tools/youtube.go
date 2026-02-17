package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type YouTubeTool struct{}

func NewYouTubeTool() *YouTubeTool {
	return &YouTubeTool{}
}

func (t *YouTubeTool) Name() string { return "youtube" }

func (t *YouTubeTool) Description() string {
	return "Extract transcript/captions from a YouTube video. Returns the text content which you can then summarize. Use when user shares a YouTube link and wants a summary."
}

func (t *YouTubeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "YouTube video URL",
			},
			"lang": map[string]interface{}{
				"type":        "string",
				"description": "Preferred caption language code (default: 'es', fallback: 'en')",
			},
		},
		"required": []string{"url"},
	}
}

func (t *YouTubeTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	videoURL, _ := args["url"].(string)
	if videoURL == "" {
		return ErrorResult("url is required")
	}

	lang := "es"
	if l, ok := args["lang"].(string); ok && l != "" {
		lang = l
	}

	videoID := extractVideoID(videoURL)
	if videoID == "" {
		return ErrorResult("could not extract video ID from URL")
	}

	transcript, err := fetchTranscript(ctx, videoID, lang)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to get transcript: %v", err))
	}

	// Truncate if too long (keep ~15k chars for LLM context)
	if len(transcript) > 15000 {
		transcript = transcript[:15000] + "\n... (transcript truncated)"
	}

	return SilentResult(fmt.Sprintf("Transcript for video %s:\n\n%s", videoID, transcript))
}

func extractVideoID(rawURL string) string {
	// Handle youtu.be/ID
	if strings.Contains(rawURL, "youtu.be/") {
		parts := strings.Split(rawURL, "youtu.be/")
		if len(parts) == 2 {
			id := strings.Split(parts[1], "?")[0]
			id = strings.Split(id, "&")[0]
			return strings.TrimSpace(id)
		}
	}

	// Handle youtube.com/watch?v=ID
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if v := parsed.Query().Get("v"); v != "" {
		return v
	}

	// Handle youtube.com/embed/ID or youtube.com/v/ID
	pathParts := strings.Split(parsed.Path, "/")
	for i, part := range pathParts {
		if (part == "embed" || part == "v") && i+1 < len(pathParts) {
			return pathParts[i+1]
		}
	}

	return ""
}

func fetchTranscript(ctx context.Context, videoID, preferredLang string) (string, error) {
	// Fetch the watch page to get caption tracks
	watchURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	req, err := http.NewRequestWithContext(ctx, "GET", watchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch watch page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read watch page: %w", err)
	}

	html := string(body)

	// Extract ytInitialPlayerResponse JSON
	re := regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*(\{.+?\});`)
	match := re.FindStringSubmatch(html)
	if len(match) < 2 {
		return "", fmt.Errorf("could not find player response in page")
	}

	var playerResp struct {
		Captions struct {
			PlayerCaptionsTracklistRenderer struct {
				CaptionTracks []struct {
					BaseURL      string `json:"baseUrl"`
					LanguageCode string `json:"languageCode"`
					Kind         string `json:"kind"`
				} `json:"captionTracks"`
			} `json:"playerCaptionsTracklistRenderer"`
		} `json:"captions"`
	}

	if err := json.Unmarshal([]byte(match[1]), &playerResp); err != nil {
		return "", fmt.Errorf("failed to parse player response: %w", err)
	}

	tracks := playerResp.Captions.PlayerCaptionsTracklistRenderer.CaptionTracks
	if len(tracks) == 0 {
		return "", fmt.Errorf("no captions available for this video")
	}

	// Find preferred language, fallback to English, then first available
	var captionURL string
	for _, track := range tracks {
		if track.LanguageCode == preferredLang {
			captionURL = track.BaseURL
			break
		}
	}
	if captionURL == "" && preferredLang != "en" {
		for _, track := range tracks {
			if track.LanguageCode == "en" {
				captionURL = track.BaseURL
				break
			}
		}
	}
	if captionURL == "" {
		captionURL = tracks[0].BaseURL
	}

	// Fetch the captions XML
	captionReq, err := http.NewRequestWithContext(ctx, "GET", captionURL, nil)
	if err != nil {
		return "", err
	}
	captionReq.Header.Set("User-Agent", userAgent)

	captionResp, err := client.Do(captionReq)
	if err != nil {
		return "", fmt.Errorf("failed to fetch captions: %w", err)
	}
	defer captionResp.Body.Close()

	captionBody, err := io.ReadAll(captionResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read captions: %w", err)
	}

	// Parse caption XML — extract text between <text> tags
	return parseCaptionXML(string(captionBody)), nil
}

func parseCaptionXML(xml string) string {
	re := regexp.MustCompile(`<text[^>]*>([\s\S]*?)</text>`)
	matches := re.FindAllStringSubmatch(xml, -1)

	var lines []string
	for _, m := range matches {
		text := m[1]
		// Decode common HTML entities
		text = strings.ReplaceAll(text, "&amp;", "&")
		text = strings.ReplaceAll(text, "&lt;", "<")
		text = strings.ReplaceAll(text, "&gt;", ">")
		text = strings.ReplaceAll(text, "&quot;", "\"")
		text = strings.ReplaceAll(text, "&#39;", "'")
		text = strings.ReplaceAll(text, "\n", " ")
		text = strings.TrimSpace(text)
		if text != "" {
			lines = append(lines, text)
		}
	}

	return strings.Join(lines, " ")
}
