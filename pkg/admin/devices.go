package admin

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// lightsDevices handles GET /api/devices/lights — list all saved lights.
func (h *Handler) lightsDevices(w http.ResponseWriter, r *http.Request) {
	if h.lightsTool == nil {
		jsonErr(w, 503, "lights tool not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := h.lightsTool.Execute(ctx, map[string]interface{}{
		"action": "list",
	})
	jsonOK(w, map[string]interface{}{"output": result.ForLLM, "error": result.IsError})
}

// lightsDiscover handles POST /api/devices/lights/discover — discover lights on the network.
func (h *Handler) lightsDiscover(w http.ResponseWriter, r *http.Request) {
	if h.lightsTool == nil {
		jsonErr(w, 503, "lights tool not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result := h.lightsTool.Execute(ctx, map[string]interface{}{
		"action": "discover",
	})
	jsonOK(w, map[string]interface{}{"output": result.ForLLM, "error": result.IsError})
}

// lightsSave handles POST /api/devices/lights/save — save a discovered light.
func (h *Handler) lightsSave(w http.ResponseWriter, r *http.Request) {
	if h.lightsTool == nil {
		jsonErr(w, 503, "lights tool not available")
		return
	}
	var body struct {
		Name       string `json:"name"`
		IP         string `json:"ip"`
		MAC        string `json:"mac"`
		Model      string `json:"model"`
		DeviceType string `json:"device_type"`
	}
	if err := decodeBody(r, &body); err != nil {
		jsonErr(w, 400, "invalid JSON body: "+err.Error())
		return
	}
	if body.Name == "" || body.IP == "" {
		jsonErr(w, 400, "name and ip are required")
		return
	}
	args := map[string]interface{}{
		"action": "save",
		"name":   body.Name,
		"ip":     body.IP,
	}
	if body.MAC != "" {
		args["mac"] = body.MAC
	}
	if body.Model != "" {
		args["model"] = body.Model
	}
	if body.DeviceType != "" {
		args["device_type"] = body.DeviceType
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := h.lightsTool.Execute(ctx, args)
	jsonOK(w, map[string]interface{}{"output": result.ForLLM, "error": result.IsError})
}

// lightsControl handles POST /api/devices/lights/control — control a light device.
func (h *Handler) lightsControl(w http.ResponseWriter, r *http.Request) {
	if h.lightsTool == nil {
		jsonErr(w, 503, "lights tool not available")
		return
	}
	var body struct {
		Device     string  `json:"device"`
		Action     string  `json:"action"`
		R          *int    `json:"r"`
		G          *int    `json:"g"`
		B          *int    `json:"b"`
		Brightness *int    `json:"brightness"`
		Pattern    *string `json:"pattern"`
		Speed      *int    `json:"speed"`
	}
	if err := decodeBody(r, &body); err != nil {
		jsonErr(w, 400, "invalid JSON body: "+err.Error())
		return
	}
	if body.Device == "" || body.Action == "" {
		jsonErr(w, 400, "device and action are required")
		return
	}
	args := map[string]interface{}{
		"action": body.Action,
		"device": body.Device,
	}
	if body.R != nil {
		args["r"] = *body.R
	}
	if body.G != nil {
		args["g"] = *body.G
	}
	if body.B != nil {
		args["b"] = *body.B
	}
	if body.Brightness != nil {
		args["brightness"] = *body.Brightness
	}
	if body.Pattern != nil {
		args["pattern"] = *body.Pattern
	}
	if body.Speed != nil {
		args["speed"] = *body.Speed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := h.lightsTool.Execute(ctx, args)
	jsonOK(w, map[string]interface{}{"output": result.ForLLM, "error": result.IsError})
}

// lightsDevice handles DELETE /api/devices/lights/{name} — remove a saved light.
func (h *Handler) lightsDevice(w http.ResponseWriter, r *http.Request) {
	if h.lightsTool == nil {
		jsonErr(w, 503, "lights tool not available")
		return
	}
	if r.Method != http.MethodDelete {
		jsonErr(w, 405, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/devices/lights/")
	if name == "" {
		jsonErr(w, 400, "device name is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := h.lightsTool.Execute(ctx, map[string]interface{}{
		"action": "remove",
		"device": name,
	})
	jsonOK(w, map[string]interface{}{"output": result.ForLLM, "error": result.IsError})
}
