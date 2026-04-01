package providers

import (
	"context"
	"strings"
	"testing"
)

type fakeCapabilityProvider struct {
	resp *LLMResponse
	caps ModelCapabilities
}

func (f *fakeCapabilityProvider) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	if f.resp != nil {
		return f.resp, nil
	}
	return &LLMResponse{Content: "ok"}, nil
}

func (f *fakeCapabilityProvider) GetDefaultModel() string {
	return f.caps.Model
}

func (f *fakeCapabilityProvider) capabilities(model string) ModelCapabilities {
	if model != "" {
		f.caps.Model = model
	}
	return f.caps
}

func TestResolveCapabilities_GPT4oSupportsVision(t *testing.T) {
	caps := ResolveCapabilities(&HTTPProvider{}, "gpt-4o")
	if caps.Vision != CapabilitySupported {
		t.Fatalf("Vision = %q, want %q", caps.Vision, CapabilitySupported)
	}
}

func TestResolveCapabilities_LlamaCppDoesNotSupportVision(t *testing.T) {
	caps := ResolveCapabilities(&LlamaCppProvider{}, "")
	if caps.Vision != CapabilityUnsupported {
		t.Fatalf("Vision = %q, want %q", caps.Vision, CapabilityUnsupported)
	}
}

func TestPrivacyRouter_LocalImageWithoutVisionAddsNotice(t *testing.T) {
	cloud := &fakeCapabilityProvider{
		caps: ModelCapabilities{Provider: "cloud", Model: "gpt-4o", Vision: CapabilitySupported},
		resp: &LLMResponse{Content: "respuesta cloud"},
	}
	local := &fakeCapabilityProvider{
		caps: ModelCapabilities{Provider: "local", Model: "qwen-local", Vision: CapabilityUnsupported},
		resp: &LLMResponse{Content: "respuesta local"},
	}
	router := NewPrivacyRouter(cloud, local, NewPrivacyClassifier(PrivacyClassifierConfig{
		AlwaysPrivateMedia: true,
	}), false)

	resp, err := router.Chat(context.Background(), []Message{
		{
			Role: "user",
			Parts: []ContentPart{
				{Type: "text", Text: "decime que ves"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,abc"}},
			},
		},
	}, nil, "gpt-4o", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if !strings.Contains(resp.Content, "no soporta vision") {
		t.Fatalf("response missing vision notice: %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "respuesta local") {
		t.Fatalf("response missing local content: %q", resp.Content)
	}
}
