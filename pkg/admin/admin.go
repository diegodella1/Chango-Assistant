package admin

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

//go:embed static/index.html
var staticFS embed.FS

// Editable files whitelist (relative to workspace)
var allowedFiles = []string{
	"SOUL.md",
	"AGENTS.md",
	"IDENTITY.md",
	"USER.md",
	"HEARTBEAT.md",
	"council/cfo.md",
	"council/cto.md",
	"council/chango.md",
}

type Handler struct {
	workspacePath string
	token         string
	configPath    string
	config        *config.Config
	cronService   *cron.CronService
	lightsTool    *tools.LightsTool
	reloadFn      func() error
	version       string
}

func New(workspacePath, token, configPath string, cfg *config.Config) *Handler {
	return &Handler{
		workspacePath: workspacePath,
		token:         token,
		configPath:    configPath,
		config:        cfg,
	}
}

func (h *Handler) SetCronService(cs *cron.CronService) { h.cronService = cs }
func (h *Handler) SetLightsTool(lt *tools.LightsTool)   { h.lightsTool = lt }
func (h *Handler) SetReloadFn(fn func() error)          { h.reloadFn = fn }
func (h *Handler) SetVersion(v string)                   { h.version = v }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/admin", h.serveSPA)
	mux.HandleFunc("/api/health", h.withAuth(h.systemHealth))
	mux.HandleFunc("/api/swap/free", h.withAuth(h.freeSwap))
	mux.HandleFunc("/api/wifi/scan", h.withAuth(h.wifiScan))
	mux.HandleFunc("/api/bluetooth/scan", h.withAuth(h.bluetoothScan))
	mux.HandleFunc("/api/files", h.withAuth(h.listFiles))
	mux.HandleFunc("/api/files/", h.withAuth(h.handleFile))
	mux.HandleFunc("/api/vault", h.withAuth(h.vaultOverview))
	mux.HandleFunc("/api/vault/notes", h.withAuth(h.vaultNotes))
	mux.HandleFunc("/api/vault/note/", h.withAuth(h.vaultNote))
	// Settings endpoints
	mux.HandleFunc("/api/settings/providers", h.withAuth(h.settingsProviders))
	mux.HandleFunc("/api/settings/channels", h.withAuth(h.settingsChannels))
	mux.HandleFunc("/api/settings/tools", h.withAuth(h.settingsTools))
	mux.HandleFunc("/api/settings/services", h.withAuth(h.settingsServices))
	mux.HandleFunc("/api/settings/agent", h.withAuth(h.settingsAgent))
	mux.HandleFunc("/api/settings/upload/google-sa", h.withAuth(h.uploadGoogleSA))
	mux.HandleFunc("/api/settings/test/provider", h.withAuth(h.testProvider))
	mux.HandleFunc("/api/settings/briefing", h.withAuth(h.settingsBriefing))
	// Agent reload
	mux.HandleFunc("/api/agent/reload", h.withAuth(h.agentReload))
	// Cron CRUD
	mux.HandleFunc("/api/cron/jobs", h.withAuth(h.cronJobs))
	mux.HandleFunc("/api/cron/jobs/", h.withAuth(h.cronJob))
	// Logs
	mux.HandleFunc("/api/logs", h.withAuth(h.getLogs))
	// Devices / Lights
	mux.HandleFunc("/api/devices/lights", h.withAuth(h.lightsDevices))
	mux.HandleFunc("/api/devices/lights/discover", h.withAuth(h.lightsDiscover))
	mux.HandleFunc("/api/devices/lights/save", h.withAuth(h.lightsSave))
	mux.HandleFunc("/api/devices/lights/control", h.withAuth(h.lightsControl))
	mux.HandleFunc("/api/devices/lights/", h.withAuth(h.lightsDevice))
	// Updates
	mux.HandleFunc("/api/updates/check", h.withAuth(h.updatesCheck))
	mux.HandleFunc("/api/updates/apply", h.withAuth(h.updatesApply))
	// Onboarding
	mux.HandleFunc("/api/onboarding/status", h.withAuth(h.onboardingStatus))
	mux.HandleFunc("/api/onboarding/profile", h.withAuth(h.onboardingProfile))
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.token != "" {
			auth := r.Header.Get("Authorization")
			expected := "Bearer " + h.token
			if !strings.EqualFold(auth, expected) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (h *Handler) serveSPA(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// fileInfo is the JSON shape returned by the list endpoint.
type fileInfo struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

// networkInfo represents a physical network interface.
type networkInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"`    // up/down
	IP      string `json:"ip"`       // first IPv4 if available
	RxBytes int64  `json:"rx_bytes"`
	TxBytes int64  `json:"tx_bytes"`
}

// bluetoothInfo represents a Bluetooth adapter.
type bluetoothInfo struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	State   string `json:"state"` // up/down
}

// extendedHealth merges sentinel data with live system info.
type extendedHealth struct {
	// Sentinel fields (embedded from JSON)
	Sentinel json.RawMessage `json:"sentinel"`
	// Swap
	SwapTotalMB   int64   `json:"swap_total_mb"`
	SwapFreeMB    int64   `json:"swap_free_mb"`
	SwapUsedPct   float64 `json:"swap_used_percent"`
	// Network
	Networks []networkInfo `json:"networks"`
	// Bluetooth
	Bluetooth []bluetoothInfo `json:"bluetooth"`
}

func (h *Handler) systemHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var result extendedHealth

	// Read sentinel data
	sentinelData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json"))
	if err == nil {
		result.Sentinel = json.RawMessage(sentinelData)
	} else {
		result.Sentinel = json.RawMessage(`{}`)
	}

	// Read swap from /proc/meminfo
	result.SwapTotalMB, result.SwapFreeMB, result.SwapUsedPct = readSwap()

	// Read network interfaces
	result.Networks = readNetworks()

	// Read bluetooth
	result.Bluetooth = readBluetooth()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// physicalInterfaces are the network interfaces we care about (not veth/bridge/docker).
var physicalInterfaces = map[string]bool{
	"eth0": true, "wlan0": true, "tailscale0": true,
}

func readSwap() (totalMB, freeMB int64, usedPct float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "SwapTotal:") {
			totalMB = parseMemInfoKB(line) / 1024
		} else if strings.HasPrefix(line, "SwapFree:") {
			freeMB = parseMemInfoKB(line) / 1024
		}
	}
	if totalMB > 0 {
		usedPct = float64(totalMB-freeMB) / float64(totalMB) * 100
	}
	return
}

func parseMemInfoKB(line string) int64 {
	// "SwapTotal:       2097136 kB"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[1], 10, 64)
	return v
}

func readNetworks() []networkInfo {
	var nets []networkInfo

	// Read RX/TX from /proc/net/dev
	rxTx := make(map[string][2]int64) // name -> [rx, tx]
	if f, err := os.Open("/proc/net/dev"); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			if !physicalInterfaces[name] {
				continue
			}
			fields := strings.Fields(parts[1])
			if len(fields) >= 9 {
				rx, _ := strconv.ParseInt(fields[0], 10, 64)
				tx, _ := strconv.ParseInt(fields[8], 10, 64)
				rxTx[name] = [2]int64{rx, tx}
			}
		}
		f.Close()
	}

	// Read state and IP for each physical interface
	for name := range physicalInterfaces {
		operstate := "down"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/operstate", name)); err == nil {
			operstate = strings.TrimSpace(string(data))
		} else {
			continue // interface doesn't exist
		}

		ip := getInterfaceIPv4(name)

		rt := rxTx[name]
		nets = append(nets, networkInfo{
			Name:    name,
			State:   operstate,
			IP:      ip,
			RxBytes: rt[0],
			TxBytes: rt[1],
		})
	}

	return nets
}

func getInterfaceIPv4(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

func readBluetooth() []bluetoothInfo {
	var bt []bluetoothInfo

	entries, err := os.ReadDir("/sys/class/bluetooth")
	if err != nil {
		return bt
	}

	seen := make(map[string]bool)
	for _, e := range entries {
		// hci0, hci0:12 — only care about base adapter
		hciName := strings.SplitN(e.Name(), ":", 2)[0]
		if seen[hciName] {
			continue
		}
		seen[hciName] = true

		addr := ""
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/bluetooth/%s/address", hciName)); err == nil {
			addr = strings.TrimSpace(string(data))
		}

		// Check if adapter is up by reading type (if readable, it's present)
		state := "down"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/bluetooth/%s/type", hciName)); err == nil && len(data) > 0 {
			state = "up"
		}

		bt = append(bt, bluetoothInfo{
			Name:    hciName,
			Address: addr,
			State:   state,
		})
	}

	return bt
}

// hostExec runs a command via nsenter in the host's mount+net namespace.
// Uses /hostfs/proc/1/ns/* when --pid=host is not available (Coolify ignores it).
func hostExec(timeout time.Duration, args ...string) ([]byte, error) {
	var nsArgs []string
	if _, err := os.Stat("/hostfs/proc/1/ns/mnt"); err == nil {
		// Coolify container: --pid=host not applied, use /hostfs/proc bind mount
		nsArgs = append([]string{"--mount=/hostfs/proc/1/ns/mnt", "--net=/hostfs/proc/1/ns/net", "--"}, args...)
	} else {
		// Fallback: assume --pid=host works
		nsArgs = append([]string{"-t", "1", "-m", "-n", "--"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "nsenter", nsArgs...).CombinedOutput()
}

func (h *Handler) freeSwap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	logger.InfoC("admin", "Freeing swap...")
	out, err := hostExec(60*time.Second, "sh", "-c", "swapoff -a && swapon -a")
	if err != nil {
		logger.ErrorCF("admin", "Free swap failed", map[string]interface{}{"error": err.Error(), "output": string(out)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "swapoff failed: " + string(out)})
		return
	}

	logger.InfoC("admin", "Swap freed successfully")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type wifiNetwork struct {
	SSID    string `json:"ssid"`
	Signal  string `json:"signal"`
	Freq    string `json:"freq"`
	BSSID   string `json:"bssid"`
	Channel string `json:"channel"`
}

var (
	reBSS     = regexp.MustCompile(`^BSS ([0-9a-f:]+)`)
	reSSID    = regexp.MustCompile(`^\s+SSID: (.+)`)
	reSignal  = regexp.MustCompile(`^\s+signal: (.+)`)
	reFreq    = regexp.MustCompile(`^\s+freq: (\d+)`)
	reChannel = regexp.MustCompile(`^\s+DS Parameter set: channel (\d+)`)
)

func (h *Handler) wifiScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out, err := hostExec(15*time.Second, "iw", "dev", "wlan0", "scan")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "wifi scan failed: " + string(out)})
		return
	}

	var networks []wifiNetwork
	var cur *wifiNetwork

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reBSS.FindStringSubmatch(line); m != nil {
			if cur != nil && cur.SSID != "" {
				networks = append(networks, *cur)
			}
			cur = &wifiNetwork{BSSID: m[1]}
		}
		if cur == nil {
			continue
		}
		if m := reSSID.FindStringSubmatch(line); m != nil {
			cur.SSID = m[1]
		} else if m := reSignal.FindStringSubmatch(line); m != nil {
			cur.Signal = m[1]
		} else if m := reFreq.FindStringSubmatch(line); m != nil {
			cur.Freq = m[1] + " MHz"
		} else if m := reChannel.FindStringSubmatch(line); m != nil {
			cur.Channel = m[1]
		}
	}
	if cur != nil && cur.SSID != "" {
		networks = append(networks, *cur)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(networks)
}

type btDevice struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

func (h *Handler) bluetoothScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out, err := hostExec(15*time.Second, "hcitool", "scan", "--length=8")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "bluetooth scan failed: " + string(out)})
		return
	}

	var devices []btDevice
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Scanning") {
			continue
		}
		// Format: "XX:XX:XX:XX:XX:XX	Device Name"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			devices = append(devices, btDevice{Address: parts[0], Name: parts[1]})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devices)
}

func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	files := make([]fileInfo, 0, len(allowedFiles))
	for _, name := range allowedFiles {
		full := filepath.Join(h.workspacePath, name)
		_, err := os.Stat(full)
		files = append(files, fileInfo{Name: name, Exists: err == nil})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	// Extract filename from /api/files/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if name == "" {
		http.Error(w, "Missing filename", http.StatusBadRequest)
		return
	}

	// Validate against whitelist (prevents path traversal)
	if !h.isAllowed(name) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Double-check: cleaned path must stay inside workspace
	full := filepath.Join(h.workspacePath, name)
	if !strings.HasPrefix(full, h.workspacePath) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.readFile(w, full)
	case http.MethodPut:
		h.writeFile(w, r, full)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) isAllowed(name string) bool {
	clean := filepath.Clean(name)
	for _, allowed := range allowedFiles {
		if clean == allowed {
			return true
		}
	}
	return false
}

func (h *Handler) readFile(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty content for files that don't exist yet
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"content": ""})
			return
		}
		logger.ErrorCF("admin", "Read file error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Read error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}

// --- Vault endpoints ---

type vaultFolderInfo struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type vaultOverviewResp struct {
	TotalNotes int               `json:"total_notes"`
	Folders    []vaultFolderInfo `json:"folders"`
}

func (h *Handler) vaultOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vaultDir := filepath.Join(h.workspacePath, "obsidian")
	folders := []string{"daily", "people", "preferences", "insights", "decisions", "projects", "blog", "state", "inbox"}

	var resp vaultOverviewResp
	for _, f := range folders {
		dir := filepath.Join(vaultDir, f)
		entries, err := os.ReadDir(dir)
		count := 0
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					count++
				}
			}
		}
		resp.Folders = append(resp.Folders, vaultFolderInfo{Name: f, Count: count})
		resp.TotalNotes += count
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type vaultNoteInfo struct {
	Key     string   `json:"key"`
	Folder  string   `json:"folder"`
	Tags    []string `json:"tags"`
	Updated string   `json:"updated"`
	Preview string   `json:"preview"`
}

func (h *Handler) vaultNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "folder param required", http.StatusBadRequest)
		return
	}

	// Sanitize folder name
	folder = filepath.Clean(folder)
	if strings.Contains(folder, "..") || strings.Contains(folder, "/") {
		http.Error(w, "invalid folder", http.StatusBadRequest)
		return
	}

	dir := filepath.Join(h.workspacePath, "obsidian", folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]vaultNoteInfo{})
		return
	}

	var notes []vaultNoteInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		key := strings.TrimSuffix(e.Name(), ".md")
		content := string(data)

		note := vaultNoteInfo{
			Key:    key,
			Folder: folder,
		}

		// Parse frontmatter
		body := content
		if strings.HasPrefix(content, "---\n") {
			rest := content[4:]
			if endIdx := strings.Index(rest, "\n---\n"); endIdx != -1 {
				fm := rest[:endIdx]
				body = strings.TrimSpace(rest[endIdx+5:])
				for _, line := range strings.Split(fm, "\n") {
					ci := strings.Index(line, ": ")
					if ci == -1 {
						continue
					}
					field, val := line[:ci], line[ci+2:]
					switch field {
					case "tags":
						val = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(val), "]"), "[")
						for _, t := range strings.Split(val, ",") {
							t = strings.TrimSpace(t)
							if t != "" {
								note.Tags = append(note.Tags, t)
							}
						}
					case "updated":
						note.Updated = val
					}
				}
			}
		}

		// Preview: first 120 chars of body
		preview := strings.ReplaceAll(body, "\n", " ")
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		note.Preview = preview
		notes = append(notes, note)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(notes)
}

func (h *Handler) vaultNote(w http.ResponseWriter, r *http.Request) {
	// /api/vault/note/{folder}/{key}
	path := strings.TrimPrefix(r.URL.Path, "/api/vault/note/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "path must be /api/vault/note/{folder}/{key}", http.StatusBadRequest)
		return
	}

	folder := filepath.Clean(parts[0])
	key := filepath.Clean(parts[1])

	if strings.Contains(folder, "..") || strings.Contains(key, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	notePath := filepath.Join(h.workspacePath, "obsidian", folder, key+".md")

	// Ensure path stays within workspace
	if !strings.HasPrefix(notePath, filepath.Join(h.workspacePath, "obsidian")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(notePath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"content": string(data)})

	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		limited := io.LimitReader(r.Body, 1<<20)
		if err := json.NewDecoder(limited).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		dir := filepath.Dir(notePath)
		os.MkdirAll(dir, 0755)

		tmp := notePath + ".tmp"
		if err := os.WriteFile(tmp, []byte(body.Content), 0644); err != nil {
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, notePath); err != nil {
			os.Remove(tmp)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}

		logger.InfoCF("admin", "Vault note saved", map[string]interface{}{"folder": folder, "key": key})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case http.MethodDelete:
		if err := os.Remove(notePath); err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "delete error", http.StatusInternalServerError)
			return
		}
		logger.InfoCF("admin", "Vault note deleted", map[string]interface{}{"folder": folder, "key": key})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) writeFile(w http.ResponseWriter, r *http.Request, path string) {
	var body struct {
		Content string `json:"content"`
	}

	// Limit to 1MB
	limited := io.LimitReader(r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	// Ensure parent dir exists (for council/*.md)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.ErrorCF("admin", "Mkdir error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}

	// Atomic write: tmp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body.Content), 0644); err != nil {
		logger.ErrorCF("admin", "Write temp file error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		logger.ErrorCF("admin", "Rename error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}

	logger.InfoCF("admin", "File saved", map[string]interface{}{"path": path})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Agent Reload ---

func (h *Handler) agentReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reloaded := []string{}

	// Re-read config from disk
	newCfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		logger.ErrorCF("admin", "Reload config failed", map[string]interface{}{"error": err.Error()})
		jsonErr(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}

	// Update config fields in-place (pointer is shared with all services)
	h.config.Agents = newCfg.Agents
	h.config.Providers = newCfg.Providers
	h.config.Tools = newCfg.Tools
	h.config.Heartbeat = newCfg.Heartbeat
	h.config.Sentinel = newCfg.Sentinel
	h.config.Devices = newCfg.Devices
	h.config.Council = newCfg.Council
	h.config.Channels = newCfg.Channels
	h.config.Briefing = newCfg.Briefing
	reloaded = append(reloaded, "config")

	// Reload cron jobs
	if h.reloadFn != nil {
		if err := h.reloadFn(); err != nil {
			logger.ErrorCF("admin", "Reload services failed", map[string]interface{}{"error": err.Error()})
		} else {
			reloaded = append(reloaded, "cron")
		}
	}

	logger.InfoCF("admin", "Agent reloaded", map[string]interface{}{"reloaded": reloaded})
	jsonOK(w, map[string]interface{}{"status": "ok", "reloaded": reloaded})
}
