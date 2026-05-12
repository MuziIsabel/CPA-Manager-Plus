package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/inspection"
	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

type codexInspectionTaskResponse struct {
	Task store.CodexInspectionTask `json:"task"`
}

type codexInspectionTasksResponse struct {
	Tasks []store.CodexInspectionTask `json:"tasks"`
	Total int                         `json:"total"`
}

type codexInspectionRunsResponse struct {
	Runs     []store.CodexInspectionRun `json:"runs"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"pageSize"`
}

type codexInspectionRunResponse struct {
	Run           store.CodexInspectionRun                  `json:"run"`
	Accounts      []store.CodexInspectionAccountResult      `json:"accounts,omitempty"`
	Actions       []store.CodexInspectionActionRecord       `json:"actions,omitempty"`
	Notifications []store.CodexInspectionNotificationRecord `json:"notifications,omitempty"`
}

type codexInspectionEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

type codexInspectionRunRequest struct {
	DryRunOverride *bool `json:"dryRunOverride"`
}

func (s *Server) handleCodexInspection(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeIfConfigured(w, r) {
		return
	}

	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v0/management/codex-inspection"), "/")
	switch {
	case path == "tasks":
		s.handleCodexInspectionTasks(w, r)
	case strings.HasPrefix(path, "tasks/"):
		s.handleCodexInspectionTaskPath(w, r, strings.TrimPrefix(path, "tasks/"))
	case path == "runs":
		s.handleCodexInspectionRuns(w, r)
	case strings.HasPrefix(path, "runs/"):
		s.handleCodexInspectionRunPath(w, r, strings.TrimPrefix(path, "runs/"))
	case path == "notifications/test":
		s.handleCodexInspectionNotificationTest(w, r)
	case path == "scheduler/status":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if s.scheduler == nil {
			writeJSON(w, http.StatusOK, inspection.SchedulerStatus{
				Status:         "not_started",
				Running:        false,
				RunningTaskIDs: []string{},
			})
			return
		}
		writeJSON(w, http.StatusOK, s.scheduler.Status())
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleCodexInspectionNotificationTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	raw, err := normalizeNotificationTestConfig(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	records := inspection.NewNotifier(nil).SendTestNotification(r.Context(), raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            allNotificationRecordsSucceeded(records),
		"notifications": records,
	})
}

func (s *Server) handleCodexInspectionTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.store.ListCodexInspectionTasks(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for i := range tasks {
			maskCodexInspectionTaskSensitiveFields(&tasks[i])
		}
		writeJSON(w, http.StatusOK, codexInspectionTasksResponse{Tasks: tasks, Total: len(tasks)})
	case http.MethodPost:
		var req store.CodexInspectionTaskInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task, err := s.store.CreateCodexInspectionTask(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task = s.refreshCodexInspectionTaskNextRun(r.Context(), task)
		maskCodexInspectionTaskSensitiveFields(&task)
		writeJSON(w, http.StatusCreated, codexInspectionTaskResponse{Task: task})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleCodexInspectionTaskPath(w http.ResponseWriter, r *http.Request, subpath string) {
	parts := strings.Split(strings.Trim(subpath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]

	if len(parts) == 1 {
		s.handleCodexInspectionTask(w, r, id)
		return
	}

	if len(parts) == 2 && parts[1] == "enabled" {
		s.handleCodexInspectionTaskEnabled(w, r, id)
		return
	}

	if len(parts) == 2 && parts[1] == "runs" {
		s.handleCodexInspectionTaskRun(w, r, id)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleCodexInspectionTaskRun(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req codexInspectionRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	task, err := s.store.GetCodexInspectionTask(r.Context(), id)
	if err != nil {
		writeStoreReadError(w, err)
		return
	}
	run, err := s.inspector.Run(r.Context(), setup, task, inspection.RunOptions{
		Trigger:        "manual",
		DryRunOverride: req.DryRunOverride,
	}, s.store)
	if err != nil && run.ID == "" {
		if errors.Is(err, store.ErrCodexInspectionTaskRunning) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	accounts, accountErr := s.store.ListCodexInspectionAccountResults(r.Context(), run.ID)
	if accountErr != nil {
		writeError(w, http.StatusInternalServerError, accountErr)
		return
	}
	actions, actionErr := s.store.ListCodexInspectionActionRecords(r.Context(), run.ID)
	if actionErr != nil {
		writeError(w, http.StatusInternalServerError, actionErr)
		return
	}
	notifications, notificationErr := s.store.ListCodexInspectionNotificationRecords(r.Context(), run.ID)
	if notificationErr != nil {
		writeError(w, http.StatusInternalServerError, notificationErr)
		return
	}
	maskCodexInspectionRunSensitiveFields(&run)
	writeJSON(w, http.StatusOK, codexInspectionRunResponse{
		Run:           run,
		Accounts:      accounts,
		Actions:       actions,
		Notifications: notifications,
	})
}

func (s *Server) handleCodexInspectionTask(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		task, err := s.store.GetCodexInspectionTask(r.Context(), id)
		if err != nil {
			writeStoreReadError(w, err)
			return
		}
		maskCodexInspectionTaskSensitiveFields(&task)
		writeJSON(w, http.StatusOK, codexInspectionTaskResponse{Task: task})
	case http.MethodPut:
		var req store.CodexInspectionTaskInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task, err := s.store.UpdateCodexInspectionTask(r.Context(), id, req)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, errors.New("task not found"))
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task = s.refreshCodexInspectionTaskNextRun(r.Context(), task)
		maskCodexInspectionTaskSensitiveFields(&task)
		writeJSON(w, http.StatusOK, codexInspectionTaskResponse{Task: task})
	case http.MethodDelete:
		if err := s.store.DeleteCodexInspectionTask(r.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, errors.New("task not found"))
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleCodexInspectionTaskEnabled(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	var req codexInspectionEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	task, err := s.store.SetCodexInspectionTaskEnabled(r.Context(), id, *req.Enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	task = s.refreshCodexInspectionTaskNextRun(r.Context(), task)
	maskCodexInspectionTaskSensitiveFields(&task)
	writeJSON(w, http.StatusOK, codexInspectionTaskResponse{Task: task})
}

func (s *Server) handleCodexInspectionRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	page := parsePositiveInt(query.Get("page"), 1)
	pageSize := parsePositiveInt(query.Get("pageSize"), 20)
	runs, total, err := s.store.ListCodexInspectionRuns(
		r.Context(),
		query.Get("taskId"),
		query.Get("status"),
		page,
		pageSize,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range runs {
		maskCodexInspectionRunSensitiveFields(&runs[i])
	}
	writeJSON(w, http.StatusOK, codexInspectionRunsResponse{
		Runs:     runs,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

func (s *Server) handleCodexInspectionRunPath(w http.ResponseWriter, r *http.Request, subpath string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.Trim(subpath, "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	run, err := s.store.GetCodexInspectionRun(r.Context(), id)
	if err != nil {
		writeStoreReadError(w, err)
		return
	}
	maskCodexInspectionRunSensitiveFields(&run)
	accounts, err := s.store.ListCodexInspectionAccountResults(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	actions, err := s.store.ListCodexInspectionActionRecords(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	notifications, err := s.store.ListCodexInspectionNotificationRecords(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, codexInspectionRunResponse{Run: run, Accounts: accounts, Actions: actions, Notifications: notifications})
}

func writeStoreReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func maskCodexInspectionTaskSensitiveFields(task *store.CodexInspectionTask) {
	if task == nil {
		return
	}
	task.Notification = maskSensitiveJSON(task.Notification)
}

func maskCodexInspectionRunSensitiveFields(run *store.CodexInspectionRun) {
	if run == nil {
		return
	}
	run.NotificationSnapshot = maskSensitiveJSON(run.NotificationSnapshot)
}

func maskSensitiveJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	masked := maskSensitiveValue(value)
	data, err := json.Marshal(masked)
	if err != nil {
		return raw
	}
	return data
}

func maskSensitiveValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveCodexInspectionKey(key) {
				if item == nil || strings.TrimSpace(strings.Trim(itemToString(item), `"`)) == "" {
					next[key] = item
				} else {
					next[key] = "********"
				}
				continue
			}
			next[key] = maskSensitiveValue(item)
		}
		return next
	case []any:
		next := make([]any, len(typed))
		for i, item := range typed {
			next[i] = maskSensitiveValue(item)
		}
		return next
	default:
		return value
	}
}

func isSensitiveCodexInspectionKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "webhook") ||
		strings.Contains(normalized, "authorization") ||
		normalized == "url" ||
		strings.HasSuffix(normalized, "url")
}

func itemToString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *Server) refreshCodexInspectionTaskNextRun(ctx context.Context, task store.CodexInspectionTask) store.CodexInspectionTask {
	var nextRunAtMS *int64
	if task.Enabled {
		next, err := inspection.NextRunMS(task.Schedule, time.Now())
		if err == nil {
			nextRunAtMS = next
		}
	}
	if int64PointersEqual(task.NextRunAtMS, nextRunAtMS) {
		return task
	}
	updated, err := s.store.UpdateCodexInspectionTaskNextRun(ctx, task.ID, nextRunAtMS)
	if err != nil {
		return task
	}
	return updated
}

func int64PointersEqual(left *int64, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func normalizeNotificationTestConfig(body []byte) (json.RawMessage, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errors.New("notification config is required")
	}
	raw := json.RawMessage(body)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if nested, ok := root["notification"]; ok {
			raw = nested
		}
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	config["enabled"] = true
	if _, ok := config["trigger"]; !ok {
		config["trigger"] = "always"
	}
	normalized, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func allNotificationRecordsSucceeded(records []store.CodexInspectionNotificationRecord) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		if record.Status != "success" {
			return false
		}
	}
	return true
}
