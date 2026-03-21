package admin

import (
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/cron"
)

type cronJobRequest struct {
	Name     string `json:"name"`
	Schedule struct {
		Kind    string `json:"kind"`
		Expr    string `json:"expr,omitempty"`
		EveryMS *int64 `json:"everyMs,omitempty"`
		AtMS    *int64 `json:"atMs,omitempty"`
		TZ      string `json:"tz,omitempty"`
	} `json:"schedule"`
	Message string `json:"message"`
	Deliver bool   `json:"deliver"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
	Enabled bool   `json:"enabled"`
}

// cronJobs handles GET /api/cron/jobs (list) and POST /api/cron/jobs (create).
func (h *Handler) cronJobs(w http.ResponseWriter, r *http.Request) {
	if h.cronService == nil {
		jsonErr(w, http.StatusServiceUnavailable, "cron service not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		jobs := h.cronService.ListJobs(true)
		jsonOK(w, jobs)

	case http.MethodPost:
		var req cronJobRequest
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Name == "" {
			jsonErr(w, http.StatusBadRequest, "name is required")
			return
		}
		if req.Schedule.Kind == "" {
			jsonErr(w, http.StatusBadRequest, "schedule.kind is required")
			return
		}

		sched := cron.CronSchedule{
			Kind:    req.Schedule.Kind,
			Expr:    req.Schedule.Expr,
			EveryMS: req.Schedule.EveryMS,
			AtMS:    req.Schedule.AtMS,
			TZ:      req.Schedule.TZ,
		}

		job, err := h.cronService.AddJob(req.Name, sched, req.Message, req.Deliver, req.Channel, req.To)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		if !req.Enabled {
			h.cronService.EnableJob(job.ID, false)
		}

		jsonOK(w, job)

	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// cronJob handles /api/cron/jobs/{id} (PUT, DELETE) and /api/cron/jobs/{id}/toggle (POST).
func (h *Handler) cronJob(w http.ResponseWriter, r *http.Request) {
	if h.cronService == nil {
		jsonErr(w, http.StatusServiceUnavailable, "cron service not available")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/cron/jobs/")
	if path == "" {
		jsonErr(w, http.StatusBadRequest, "job ID required")
		return
	}

	// Check for /toggle suffix
	if strings.HasSuffix(path, "/toggle") {
		jobID := strings.TrimSuffix(path, "/toggle")
		if r.Method != http.MethodPost {
			jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		job := h.cronService.EnableJob(jobID, false) // toggled below
		if job == nil {
			jsonErr(w, http.StatusNotFound, "job not found")
			return
		}
		// EnableJob with !current state
		toggled := h.cronService.EnableJob(jobID, !job.Enabled)
		jsonOK(w, toggled)
		return
	}

	jobID := path

	switch r.Method {
	case http.MethodPut:
		var req cronJobRequest
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		// Find the existing job
		jobs := h.cronService.ListJobs(true)
		var existing *cron.CronJob
		for i := range jobs {
			if jobs[i].ID == jobID {
				existing = &jobs[i]
				break
			}
		}
		if existing == nil {
			jsonErr(w, http.StatusNotFound, "job not found")
			return
		}

		if req.Name != "" {
			existing.Name = req.Name
		}
		if req.Schedule.Kind != "" {
			existing.Schedule = cron.CronSchedule{
				Kind:    req.Schedule.Kind,
				Expr:    req.Schedule.Expr,
				EveryMS: req.Schedule.EveryMS,
				AtMS:    req.Schedule.AtMS,
				TZ:      req.Schedule.TZ,
			}
		}
		existing.Payload.Message = req.Message
		existing.Payload.Deliver = req.Deliver
		existing.Payload.Channel = req.Channel
		existing.Payload.To = req.To
		existing.Enabled = req.Enabled

		if err := h.cronService.UpdateJob(existing); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, existing)

	case http.MethodDelete:
		if !h.cronService.RemoveJob(jobID) {
			jsonErr(w, http.StatusNotFound, "job not found")
			return
		}
		jsonOK(w, map[string]string{"status": "deleted"})

	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
