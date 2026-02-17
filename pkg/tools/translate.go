package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type TranslateTool struct{}

func NewTranslateTool() *TranslateTool {
	return &TranslateTool{}
}

func (t *TranslateTool) Name() string { return "translate" }

func (t *TranslateTool) Description() string {
	return "Translate text between languages. Use language codes like 'en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh', 'ko', 'ru', etc."
}

func (t *TranslateTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to translate (max 500 characters)",
			},
			"from": map[string]interface{}{
				"type":        "string",
				"description": "Source language code (default: 'auto' for auto-detection)",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Target language code (default: 'es')",
			},
		},
		"required": []string{"text"},
	}
}

func (t *TranslateTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	text, _ := args["text"].(string)
	if text == "" {
		return ErrorResult("text is required")
	}

	from := "autodetect"
	to := "es"
	if f, ok := args["from"].(string); ok && f != "" && f != "auto" {
		from = f
	}
	if toArg, ok := args["to"].(string); ok && toArg != "" {
		to = toArg
	}

	// Truncate to 500 chars
	if len(text) > 500 {
		text = text[:500]
	}

	langPair := fmt.Sprintf("%s|%s", from, to)
	apiURL := fmt.Sprintf("https://api.mymemory.translated.net/get?q=%s&langpair=%s",
		url.QueryEscape(text), url.QueryEscape(langPair))

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("translation request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	var apiResp struct {
		ResponseData struct {
			TranslatedText string  `json:"translatedText"`
			Match          float64 `json:"match"`
		} `json:"responseData"`
		ResponseStatus int `json:"responseStatus"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse response: %v", err))
	}

	if apiResp.ResponseStatus != 200 {
		return ErrorResult(fmt.Sprintf("translation API returned status %d", apiResp.ResponseStatus))
	}

	result := fmt.Sprintf("Translation (%s → %s):\n%s\n\nMatch quality: %.0f%%",
		from, to, apiResp.ResponseData.TranslatedText, apiResp.ResponseData.Match*100)

	return SilentResult(result)
}
