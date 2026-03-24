package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AudioTool plays audio through the Raspberry Pi speakers (including Bluetooth).
type AudioTool struct{}

func NewAudioTool() *AudioTool { return &AudioTool{} }

func (t *AudioTool) Name() string { return "audio" }

func (t *AudioTool) Description() string {
	return "Play audio through the Raspberry Pi speakers or connected Bluetooth device. Supports YouTube URLs, MP3 links, and local files. Control playback, volume, and audio output."
}

func (t *AudioTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"play", "stop", "pause", "resume", "volume", "sinks", "set_sink"},
				"description": "Action: play (URL/file), stop, pause, resume, volume (get/set 0-100), sinks (list outputs), set_sink (change output)",
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL or file path to play. Supports YouTube, MP3 links, local files.",
			},
			"volume": map[string]interface{}{
				"type":        "integer",
				"description": "Volume level 0-100. Omit to get current volume.",
			},
			"sink": map[string]interface{}{
				"type":        "string",
				"description": "Audio sink name or ID (from sinks action).",
			},
		},
		"required": []string{"action"},
	}
}

func (t *AudioTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)

	switch action {
	case "play":
		url, _ := args["url"].(string)
		if url == "" {
			return ErrorResult("url required for play action")
		}
		return t.play(url)
	case "stop":
		return t.stop()
	case "pause":
		return t.pause()
	case "resume":
		return t.resume()
	case "volume":
		if vol, ok := args["volume"].(float64); ok {
			return t.setVolume(int(vol))
		}
		return t.getVolume()
	case "sinks":
		return t.listSinks()
	case "set_sink":
		sink, _ := args["sink"].(string)
		if sink == "" {
			return ErrorResult("sink required for set_sink action")
		}
		return t.setSink(sink)
	default:
		return ErrorResult("unknown action: " + action)
	}
}

func (t *AudioTool) play(url string) *ToolResult {
	// Kill any existing mpv, then play in background
	// Use XDG_RUNTIME_DIR for PulseAudio/PipeWire access as the pi user
	cmd := fmt.Sprintf(
		`pkill -f 'mpv --no-video' 2>/dev/null; `+
			`su - diego -c 'XDG_RUNTIME_DIR=/run/user/1000 nohup mpv --no-video --really-quiet "%s" >/dev/null 2>&1 &'`,
		strings.ReplaceAll(url, "'", "'\\''"),
	)
	out, err := btExec(10*time.Second, cmd)
	if err != nil {
		return ErrorResult("play failed: " + out)
	}
	return SilentResult("Playing: " + url)
}

func (t *AudioTool) stop() *ToolResult {
	out, err := btExec(5*time.Second, "pkill -f 'mpv --no-video' 2>/dev/null; echo ok")
	if err != nil {
		return ErrorResult("stop failed: " + out)
	}
	return SilentResult("Playback stopped.")
}

func (t *AudioTool) pause() *ToolResult {
	out, err := btExec(5*time.Second, "pkill -STOP -f 'mpv --no-video' 2>/dev/null; echo ok")
	if err != nil {
		return ErrorResult("pause failed: " + out)
	}
	return SilentResult("Playback paused.")
}

func (t *AudioTool) resume() *ToolResult {
	out, err := btExec(5*time.Second, "pkill -CONT -f 'mpv --no-video' 2>/dev/null; echo ok")
	if err != nil {
		return ErrorResult("resume failed: " + out)
	}
	return SilentResult("Playback resumed.")
}

func (t *AudioTool) getVolume() *ToolResult {
	out, err := btExec(5*time.Second,
		`su - diego -c 'XDG_RUNTIME_DIR=/run/user/1000 pactl get-sink-volume @DEFAULT_SINK@' 2>/dev/null`)
	if err != nil {
		return ErrorResult("get volume failed: " + out)
	}
	return SilentResult(strings.TrimSpace(out))
}

func (t *AudioTool) setVolume(vol int) *ToolResult {
	if vol < 0 || vol > 100 {
		return ErrorResult("volume must be 0-100")
	}
	cmd := fmt.Sprintf(
		`su - diego -c 'XDG_RUNTIME_DIR=/run/user/1000 pactl set-sink-volume @DEFAULT_SINK@ %d%%'`, vol)
	out, err := btExec(5*time.Second, cmd)
	if err != nil {
		return ErrorResult("set volume failed: " + out)
	}
	return SilentResult(fmt.Sprintf("Volume set to %d%%", vol))
}

func (t *AudioTool) listSinks() *ToolResult {
	out, err := btExec(5*time.Second,
		`su - diego -c 'XDG_RUNTIME_DIR=/run/user/1000 pactl list sinks short' 2>/dev/null`)
	if err != nil {
		return ErrorResult("list sinks failed: " + out)
	}
	return SilentResult("Audio outputs:\n" + strings.TrimSpace(out))
}

func (t *AudioTool) setSink(sink string) *ToolResult {
	cmd := fmt.Sprintf(
		`su - diego -c 'XDG_RUNTIME_DIR=/run/user/1000 pactl set-default-sink %s'`,
		strings.ReplaceAll(sink, "'", "'\\''"))
	out, err := btExec(5*time.Second, cmd)
	if err != nil {
		return ErrorResult("set sink failed: " + out)
	}
	return SilentResult("Default audio output set to: " + sink)
}
