package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/torwin-job/musik/player/internal/db"
)

func (s *Server) handleLibraryRescan(w http.ResponseWriter, _ *http.Request) {
	s.ensureWorkerBeforeEnqueue()
	out, code, err := s.proxyWorker("POST", "/jobs", map[string]any{"kind": "full_rescan"})
	if err != nil {
		id, e2 := s.Store.EnqueueJob("full_rescan", "")
		if e2 != nil {
			writeErr(w, 503, "enqueue_failed", "worker unreachable and local enqueue failed: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "id": id, "job_id": id, "status": "pending",
			"hint": "start `musik worker` to process jobs",
		})
		return
	}
	if code >= 400 {
		writeErr(w, code, "worker_error", "worker error")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleEnqueueJob(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		writeErr(w, 400, "kind_required", "kind required")
		return
	}
	if p := caller(r); p != nil && p.DeviceToken && kind != "mix_pack" {
		writeErr(w, http.StatusForbidden, "device_token_scope", "device tokens can only refresh personal mixes")
		return
	}
	s.ensureWorkerBeforeEnqueue()
	payload := map[string]any{"profile_id": s.Store.DB.ProfileID}
	body, _ := json.Marshal(payload)
	out, code, err := s.proxyWorker("POST", "/jobs", map[string]any{"kind": kind, "payload": payload})
	if err != nil {
		id, e2 := s.Store.EnqueueJob(kind, string(body))
		if e2 != nil {
			writeErr(w, 503, "enqueue_failed", err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "id": id, "job_id": id, "status": "pending", "via": "local_db",
		})
		return
	}
	if code >= 400 {
		log.Printf("worker enqueue %s status %d", kind, code)
	}
	writeJSON(w, out)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	jobs, err := s.Store.ListJobs(status, limit)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if jobs == nil {
		jobs = []db.Job{}
	}
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobPublic(j))
	}
	writeJSON(w, map[string]any{"jobs": out, "count": len(out)})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	if p := caller(r); p != nil && p.DeviceToken {
		job, err := s.Store.GetJob(id)
		if err != nil {
			writeErr(w, 500, "db", "job unavailable")
			return
		}
		var payload struct {
			ProfileID string `json:"profile_id"`
		}
		if job == nil || job.Kind != "mix_pack" || json.Unmarshal([]byte(job.Payload), &payload) != nil || payload.ProfileID != p.Profile.ID {
			writeErr(w, 404, "not_found", "not found")
			return
		}
		writeJSON(w, jobPublic(*job))
		return
	}
	out, code, err := s.proxyWorker("GET", "/jobs/"+strconv.FormatInt(id, 10), nil)
	if err == nil && code < 400 && out != nil {
		if res, ok := out["result"].(map[string]any); ok {
			if prog, ok := res["progress"]; ok {
				out["progress"] = prog
			}
		}
		writeJSON(w, out)
		return
	}
	j, err := s.Store.GetJob(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if j == nil {
		writeErr(w, 404, "not_found", "not found")
		return
	}
	writeJSON(w, jobPublic(*j))
}

func jobPublic(j db.Job) map[string]any {
	m := map[string]any{
		"id": j.ID, "kind": j.Kind, "status": j.Status,
		"error": j.Error, "created_at": j.CreatedAt, "updated_at": j.UpdatedAt,
	}
	if j.Result != "" {
		var parsed any
		if json.Unmarshal([]byte(j.Result), &parsed) == nil {
			m["result"] = parsed
			if pm, ok := parsed.(map[string]any); ok {
				if prog, ok := pm["progress"]; ok {
					m["progress"] = prog
				}
			}
		} else {
			m["result_json"] = j.Result
		}
	}
	return m
}

func (s *Server) proxyWorker(method, path string, body any) (map[string]any, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.Cfg.WorkerURL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode, nil
}
