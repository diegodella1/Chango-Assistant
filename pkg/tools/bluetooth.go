package tools

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// BluetoothTool manages Bluetooth devices on the Raspberry Pi host via nsenter.
type BluetoothTool struct{}

func NewBluetoothTool() *BluetoothTool { return &BluetoothTool{} }

func (t *BluetoothTool) Name() string { return "bluetooth" }

func (t *BluetoothTool) Description() string {
	return "Manage Bluetooth devices on the Raspberry Pi. Scan for nearby devices, connect to speakers/headphones, disconnect, or check status."
}

func (t *BluetoothTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"scan", "devices", "connect", "disconnect", "remove", "status"},
				"description": "Action: scan (discover nearby, ~8s), devices (list known), connect/disconnect/remove (by address), status (device info)",
			},
			"address": map[string]interface{}{
				"type":        "string",
				"description": "Device MAC address (XX:XX:XX:XX:XX:XX). Required for connect, disconnect, remove, status.",
			},
		},
		"required": []string{"action"},
	}
}

var btMACRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

func (t *BluetoothTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	address, _ := args["address"].(string)

	switch action {
	case "scan":
		return t.scan()
	case "devices":
		return t.listDevices()
	case "connect":
		if !btMACRegex.MatchString(address) {
			return ErrorResult("address required (format: XX:XX:XX:XX:XX:XX)")
		}
		return t.connect(address)
	case "disconnect":
		if !btMACRegex.MatchString(address) {
			return ErrorResult("address required (format: XX:XX:XX:XX:XX:XX)")
		}
		return t.disconnect(address)
	case "remove":
		if !btMACRegex.MatchString(address) {
			return ErrorResult("address required (format: XX:XX:XX:XX:XX:XX)")
		}
		return t.remove(address)
	case "status":
		if !btMACRegex.MatchString(address) {
			return ErrorResult("address required (format: XX:XX:XX:XX:XX:XX)")
		}
		return t.status(address)
	default:
		return ErrorResult("unknown action: " + action)
	}
}

func (t *BluetoothTool) scan() *ToolResult {
	// Trigger scan (8 seconds)
	btExec(12*time.Second, "bluetoothctl --timeout 8 scan on >/dev/null 2>&1")

	// List with status
	out, err := btExec(10*time.Second, `
bluetoothctl -- devices | while IFS= read -r _ addr rest; do
  info=$(bluetoothctl -- info "$addr" 2>/dev/null)
  paired=$(echo "$info" | grep -c "Paired: yes")
  connected=$(echo "$info" | grep -c "Connected: yes")
  echo "$addr $rest [paired=$paired connected=$connected]"
done`)
	if err != nil {
		return ErrorResult("scan failed: " + out)
	}
	if strings.TrimSpace(out) == "" {
		return SilentResult("No Bluetooth devices found nearby.")
	}
	return SilentResult("Bluetooth devices found:\n" + strings.TrimSpace(out))
}

func (t *BluetoothTool) listDevices() *ToolResult {
	out, err := btExec(10*time.Second, `
bluetoothctl -- paired-devices | while IFS= read -r _ addr rest; do
  info=$(bluetoothctl -- info "$addr" 2>/dev/null)
  connected=$(echo "$info" | grep -c "Connected: yes")
  echo "$addr $rest [connected=$connected]"
done`)
	if err != nil {
		return ErrorResult("failed to list devices: " + out)
	}
	if strings.TrimSpace(out) == "" {
		return SilentResult("No paired Bluetooth devices.")
	}
	return SilentResult("Paired Bluetooth devices:\n" + strings.TrimSpace(out))
}

func (t *BluetoothTool) connect(addr string) *ToolResult {
	// Pair first (ignores error if already paired)
	btExec(10*time.Second, "bluetoothctl -- pair "+addr+" 2>/dev/null")
	// Trust (auto-reconnect)
	btExec(5*time.Second, "bluetoothctl -- trust "+addr+" 2>/dev/null")
	// Connect
	out, err := btExec(15*time.Second, "bluetoothctl -- connect "+addr)
	if err != nil {
		return ErrorResult("connect failed: " + out)
	}
	return SilentResult("Connected to " + addr)
}

func (t *BluetoothTool) disconnect(addr string) *ToolResult {
	out, err := btExec(10*time.Second, "bluetoothctl -- disconnect "+addr)
	if err != nil {
		return ErrorResult("disconnect failed: " + out)
	}
	return SilentResult("Disconnected from " + addr)
}

func (t *BluetoothTool) remove(addr string) *ToolResult {
	out, err := btExec(10*time.Second, "bluetoothctl -- remove "+addr)
	if err != nil {
		return ErrorResult("remove failed: " + out)
	}
	return SilentResult("Removed " + addr)
}

func (t *BluetoothTool) status(addr string) *ToolResult {
	out, err := btExec(5*time.Second, "bluetoothctl -- info "+addr)
	if err != nil {
		return ErrorResult("status failed: " + out)
	}
	return SilentResult(strings.TrimSpace(out))
}

// btExec runs a shell command on the host via nsenter with full namespace access.
func btExec(timeout time.Duration, shellCmd string) (string, error) {
	var nsArgs []string
	if _, err := os.Stat("/hostfs/proc/1/ns/mnt"); err == nil {
		nsArgs = []string{
			"--mount=/hostfs/proc/1/ns/mnt",
			"--uts=/hostfs/proc/1/ns/uts",
			"--ipc=/hostfs/proc/1/ns/ipc",
			"--net=/hostfs/proc/1/ns/net",
			"--", "sh", "-c", shellCmd,
		}
	} else {
		nsArgs = []string{"-t", "1", "-m", "-u", "-i", "-n", "--", "sh", "-c", shellCmd}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nsenter", nsArgs...).CombinedOutput()
	return string(out), err
}
