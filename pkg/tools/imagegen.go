package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ImageGenTool struct{}

func NewImageGenTool() *ImageGenTool {
	return &ImageGenTool{}
}

func (t *ImageGenTool) Name() string { return "image_gen" }

func (t *ImageGenTool) Description() string {
	return "Generate an image from a text prompt using AI. Returns a URL to the generated image. Use when the user asks to create, draw, or generate an image."
}

func (t *ImageGenTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the image to generate (in English for best results)",
			},
			"width": map[string]interface{}{
				"type":        "integer",
				"description": "Image width in pixels (default 1024)",
			},
			"height": map[string]interface{}{
				"type":        "integer",
				"description": "Image height in pixels (default 1024)",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *ImageGenTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return ErrorResult("prompt is required")
	}

	width := 1024
	height := 1024
	if w, ok := args["width"].(float64); ok && w > 0 {
		width = int(w)
	}
	if h, ok := args["height"].(float64); ok && h > 0 {
		height = int(h)
	}

	imageURL := fmt.Sprintf("https://image.pollinations.ai/prompt/%s?width=%d&height=%d&nologo=true",
		url.PathEscape(prompt), width, height)

	// Verify the URL works with retries (Pollinations can be flaky)
	client := &http.Client{Timeout: 60 * time.Second}
	maxAttempts := 3
	var lastErr string

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ErrorResult(fmt.Sprintf("Image generation cancelled: %v", ctx.Err()))
			case <-time.After(5 * time.Second):
			}
		}

		resp, err := client.Get(imageURL)
		if err != nil {
			lastErr = fmt.Sprintf("request failed: %v", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			lastErr = fmt.Sprintf("unexpected content-type %q", contentType)
			continue
		}

		// Success
		return MediaResult(
			fmt.Sprintf("Image generated successfully for prompt: %q", prompt),
			[]string{imageURL},
		)
	}

	return ErrorResult(fmt.Sprintf("Image generation failed after %d attempts: %s", maxAttempts, lastErr))
}
