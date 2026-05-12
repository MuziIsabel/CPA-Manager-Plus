package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager/usage-service/internal/collector"
	"github.com/seakee/cpa-manager/usage-service/internal/config"
	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

type observedRequest struct {
	path  string
	query string
	auth  string
}

func newTestHandler(t *testing.T, upstreamURL string, saveSetup bool) http.Handler {
	t.Helper()

	cfg := config.Config{
		DBPath:      filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:       "usage",
		PopSide:     "right",
		CORSOrigins: []string{"*"},
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if saveSetup {
		err := db.SaveSetup(context.Background(), store.Setup{
			CPAUpstreamURL: upstreamURL,
			ManagementKey:  "management-key",
			Queue:          "usage",
			PopSide:        "right",
		})
		if err != nil {
			t.Fatalf("save setup: %v", err)
		}
	}

	manager := collector.NewManager(cfg, db)
	return New(cfg, db, manager).Handler()
}

func newTestHandlerWithConfig(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()

	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(t.TempDir(), "usage.sqlite")
	}
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = []string{"*"}
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	manager := collector.NewManager(cfg, db)
	return New(cfg, db, manager).Handler()
}

func TestModelListProxyPreservesAuthorization(t *testing.T) {
	for _, path := range []string{"/v1/models", "/models"} {
		t.Run(path, func(t *testing.T) {
			observed := make(chan observedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- observedRequest{
					path:  r.URL.Path,
					query: r.URL.RawQuery,
					auth:  r.Header.Get("Authorization"),
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
			}))
			t.Cleanup(upstream.Close)

			handler := newTestHandler(t, upstream.URL, true)
			req := httptest.NewRequest(http.MethodGet, path+"?limit=20", nil)
			req.Header.Set("Authorization", "Bearer upstream-key")
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "gpt-4o") {
				t.Fatalf("response body = %s", rr.Body.String())
			}

			var got observedRequest
			select {
			case got = <-observed:
			default:
				t.Fatal("upstream was not called")
			}
			if got.path != path {
				t.Fatalf("proxied path = %q, want %q", got.path, path)
			}
			if got.query != "limit=20" {
				t.Fatalf("proxied query = %q, want limit=20", got.query)
			}
			if got.auth != "Bearer upstream-key" {
				t.Fatalf("proxied authorization = %q", got.auth)
			}
		})
	}
}

func TestUsageImportAcceptsLegacyExportAndSkipsDuplicates(t *testing.T) {
	handler := newTestHandler(t, "http://example.test", true)
	payload := `{
	  "version": 1,
	  "exported_at": "2026-01-02T03:04:05Z",
	  "usage": {
	    "apis": {
	      "POST /v1/chat/completions": {
	        "models": {
	          "gpt-4o": {
	            "details": [
	              {
	                "timestamp": "2026-01-02T03:04:05Z",
	                "source": "alice@example.com",
	                "auth_index": "auth-1",
	                "tokens": {
	                  "input_tokens": 10,
	                  "output_tokens": 20,
	                  "total_tokens": 30
	                },
	                "failed": false
	              }
	            ]
	          }
	        }
	      }
	    }
	  }
	}`

	first := postUsageImport(t, handler, payload)
	if first.Format != "legacy_usage_export" || first.Added != 1 || first.Skipped != 0 || first.Total != 1 {
		t.Fatalf("first import = %#v", first)
	}
	if len(first.Warnings) == 0 {
		t.Fatalf("expected legacy warnings: %#v", first)
	}

	second := postUsageImport(t, handler, payload)
	if second.Format != "legacy_usage_export" || second.Added != 0 || second.Skipped != 1 || second.Total != 1 {
		t.Fatalf("second import = %#v", second)
	}
}

func postUsageImport(t *testing.T, handler http.Handler, payload string) struct {
	Format      string   `json:"format"`
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Total       int      `json:"total"`
	Failed      int      `json:"failed"`
	Unsupported int      `json:"unsupported"`
	Warnings    []string `json:"warnings"`
} {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("import status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Format      string   `json:"format"`
		Added       int      `json:"added"`
		Skipped     int      `json:"skipped"`
		Total       int      `json:"total"`
		Failed      int      `json:"failed"`
		Unsupported int      `json:"unsupported"`
		Warnings    []string `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func TestModelListProxyRequiresSetup(t *testing.T) {
	handler := newTestHandler(t, "", false)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "usage service is not configured") {
		t.Fatalf("response body = %s", rr.Body.String())
	}
}

func TestSetupRejectsDifferentUpstreamWithoutExistingAuthorization(t *testing.T) {
	currentUpstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(currentUpstream.Close)

	nextValidationCalled := make(chan struct{}, 1)
	nextUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case nextValidationCalled <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(nextUpstream.Close)

	handler := newTestHandler(t, currentUpstream.URL, true)
	req := httptest.NewRequest(
		http.MethodPost,
		"/setup",
		bytes.NewBufferString(`{"cpaBaseUrl":"`+nextUpstream.URL+`","managementKey":"rotated-key"}`),
	)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("setup status = %d, body = %s", rr.Code, rr.Body.String())
	}
	select {
	case <-nextValidationCalled:
		t.Fatal("new upstream should not be validated without existing setup authorization")
	default:
	}
}

func TestSetupAllowsKeyRotationForSameUpstreamWithValidNewKey(t *testing.T) {
	observed := make(chan observedRequest, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/config" {
			observed <- observedRequest{
				path: r.URL.Path,
				auth: r.Header.Get("Authorization"),
			}
		}
		if r.URL.Path == "/v0/management/config" && r.Header.Get("Authorization") == "Bearer rotated-key" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, true)
	req := httptest.NewRequest(
		http.MethodPost,
		"/setup",
		bytes.NewBufferString(`{"cpaBaseUrl":"`+upstream.URL+`","managementKey":"rotated-key"}`),
	)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("setup status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got := <-observed
	if got.path != "/v0/management/config" {
		t.Fatalf("validation path = %q", got.path)
	}
	if got.auth != "Bearer rotated-key" {
		t.Fatalf("validation authorization = %q", got.auth)
	}

	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer rotated-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status after rotation = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestSetupRejectsKeyRotationWhenSetupIsEnvironmentManaged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/config" && r.Header.Get("Authorization") == "Bearer rotated-key" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandlerWithConfig(t, config.Config{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "env-key",
		Queue:          "usage",
		PopSide:        "right",
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/setup",
		bytes.NewBufferString(`{"cpaBaseUrl":"`+upstream.URL+`","managementKey":"rotated-key"}`),
	)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("setup status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "environment") {
		t.Fatalf("response body = %s", rr.Body.String())
	}
}

func TestModelPricesSaveAndLoad(t *testing.T) {
	handler := newTestHandler(t, "http://example.test", true)
	body := bytes.NewBufferString(`{"prices":{"gpt-test":{"prompt":1.25,"completion":2.5,"cache":0.1}}}`)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/model-prices", body)
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/model-prices", nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("load status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Prices map[string]struct {
			Prompt     float64 `json:"prompt"`
			Completion float64 `json:"completion"`
			Cache      float64 `json:"cache"`
		} `json:"prices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	price, ok := response.Prices["gpt-test"]
	if !ok {
		t.Fatalf("missing saved price: %#v", response.Prices)
	}
	if price.Prompt != 1.25 || price.Completion != 2.5 || price.Cache != 0.1 {
		t.Fatalf("price = %#v", price)
	}
}

func TestModelPricesSyncFromLiteLLMFormat(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"sample_spec": {},
			"gpt-test": {
				"input_cost_per_token": 0.00000125,
				"output_cost_per_token": 0.0000025,
				"cache_read_input_token_cost": 0.0000001,
				"mode": "chat"
			},
			"image-only": {
				"output_cost_per_image": 0.04,
				"mode": "image_generation"
			}
		}`))
	}))
	t.Cleanup(source.Close)
	oldURL := modelPriceSyncURL
	modelPriceSyncURL = source.URL
	t.Cleanup(func() {
		modelPriceSyncURL = oldURL
	})

	handler := newTestHandler(t, "http://example.test", true)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/model-prices/sync",
		bytes.NewBufferString(`{"models":["gpt-test"]}`),
	)
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Source   string `json:"source"`
		Imported int    `json:"imported"`
		Skipped  int    `json:"skipped"`
		Prices   map[string]struct {
			Prompt        float64 `json:"prompt"`
			Completion    float64 `json:"completion"`
			Cache         float64 `json:"cache"`
			Source        string  `json:"source"`
			SourceModelID string  `json:"sourceModelId"`
		} `json:"prices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Source != "litellm" || response.Imported != 1 || response.Skipped != 2 {
		t.Fatalf("sync summary = %#v", response)
	}
	price, ok := response.Prices["gpt-test"]
	if !ok {
		t.Fatalf("missing synced price: %#v", response.Prices)
	}
	if !closeFloat(price.Prompt, 1.25) || !closeFloat(price.Completion, 2.5) || !closeFloat(price.Cache, 0.1) {
		t.Fatalf("price = %#v", price)
	}
	if price.Source != "litellm" || price.SourceModelID != "gpt-test" {
		t.Fatalf("source metadata = %#v", price)
	}
}

func TestCodexInspectionTaskCRUDMasksSensitiveNotification(t *testing.T) {
	handler := newTestHandler(t, "http://example.test", true)
	body := bytes.NewBufferString(`{
		"name": "每日巡检 - 全部账号",
		"enabled": true,
		"targetScope": {"type": "all_codex"},
		"schedule": {"type": "daily_times", "times": ["09:00"], "timezone": "Asia/Shanghai"},
		"notification": {
			"enabled": true,
			"channels": ["telegram"],
			"channelConfigs": {
				"telegram": {
					"botToken": "secret-token",
					"webhookUrl": "https://example.test/hook",
					"chatId": "123"
				}
			}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/tasks", body)
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret-token") || strings.Contains(rr.Body.String(), "https://example.test/hook") {
		t.Fatalf("create response leaked sensitive notification config: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "********") {
		t.Fatalf("create response did not include masked value: %s", rr.Body.String())
	}

	var created struct {
		Task struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"task"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Task.ID == "" {
		t.Fatal("created task id is empty")
	}
	if !created.Task.Enabled {
		t.Fatal("created task should be enabled")
	}

	req = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-inspection/tasks/"+created.Task.ID+"/enabled", bytes.NewBufferString(`{"enabled":false}`))
	req.Header.Set("Authorization", "Bearer management-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("disable response = %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/codex-inspection/tasks", nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"total":1`) {
		t.Fatalf("list response = %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret-token") {
		t.Fatalf("list response leaked sensitive notification config: %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-inspection/tasks/"+created.Task.ID, nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/codex-inspection/tasks", nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list after delete status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"total":0`) {
		t.Fatalf("list after delete response = %s", rr.Body.String())
	}
}

func TestCodexInspectionManualRunPersistsAccountResults(t *testing.T) {
	var apiCallAuthIndex string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{
				"files": [
					{
						"name": "codex-alpha.json",
						"auth_index": "codex-1",
						"account": "alpha@example.com",
						"provider": "codex",
						"disabled": false,
						"chatgpt_account_id": "chatgpt-account-1"
					},
					{
						"name": "claude.json",
						"auth_index": "claude-1",
						"account": "claude@example.com",
						"provider": "claude"
					}
				]
			}`))
		case "/v0/management/api-call":
			var payload struct {
				AuthIndex string            `json:"authIndex"`
				Method    string            `json:"method"`
				URL       string            `json:"url"`
				Header    map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			apiCallAuthIndex = payload.AuthIndex
			if payload.Header["Chatgpt-Account-Id"] != "chatgpt-account-1" {
				http.Error(w, "missing account header", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{
				"status_code": 200,
				"body": {
					"rate_limit": {
						"allowed": false,
						"primary_window": {
							"used_percent": 75,
							"limit_window_seconds": 18000
						},
						"secondary_window": {
							"used_percent": 100,
							"limit_window_seconds": 604800
						}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, true)
	createReq := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/codex-inspection/tasks",
		bytes.NewBufferString(`{
			"name": "手动巡检测试",
			"targetScope": {"type": "all_codex"},
			"execution": {"concurrency": 1, "timeoutMs": 1000, "retries": 0}
		}`),
	)
	createReq.Header.Set("Authorization", "Bearer management-key")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRR.Code, createRR.Body.String())
	}
	var created codexInspectionTaskResponse
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Task.ID == "" {
		t.Fatal("created task id is empty")
	}

	runReq := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/codex-inspection/tasks/"+created.Task.ID+"/runs",
		bytes.NewBufferString(`{"dryRunOverride":true}`),
	)
	runReq.Header.Set("Authorization", "Bearer management-key")
	runRR := httptest.NewRecorder()
	handler.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runRR.Code, runRR.Body.String())
	}
	if apiCallAuthIndex != "codex-1" {
		t.Fatalf("api-call auth index = %q, want codex-1", apiCallAuthIndex)
	}

	var runResponse codexInspectionRunResponse
	if err := json.Unmarshal(runRR.Body.Bytes(), &runResponse); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if runResponse.Run.Status != "success" {
		t.Fatalf("run status = %q, body = %s", runResponse.Run.Status, runRR.Body.String())
	}
	if len(runResponse.Accounts) != 1 {
		t.Fatalf("len(accounts) = %d, want 1; body = %s", len(runResponse.Accounts), runRR.Body.String())
	}
	account := runResponse.Accounts[0]
	if account.FileName != "codex-alpha.json" || account.AuthIndex != "codex-1" {
		t.Fatalf("account result identity = %#v", account)
	}
	if account.Classification != "zero_quota" || account.RecommendedAction != "disable" {
		t.Fatalf("account result decision = %#v", account)
	}

	detailReq := httptest.NewRequest(
		http.MethodGet,
		"/v0/management/codex-inspection/runs/"+runResponse.Run.ID,
		nil,
	)
	detailReq.Header.Set("Authorization", "Bearer management-key")
	detailRR := httptest.NewRecorder()
	handler.ServeHTTP(detailRR, detailReq)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detailRR.Code, detailRR.Body.String())
	}
	if !strings.Contains(detailRR.Body.String(), `"classification":"zero_quota"`) {
		t.Fatalf("detail response missing persisted account result: %s", detailRR.Body.String())
	}
}

func TestCodexInspectionManualRunAutoDisablesZeroQuotaAccount(t *testing.T) {
	var patchPayload struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}
	patchCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"files":[{"name":"codex-zero.json","auth_index":"codex-zero","account":"zero@example.com","provider":"codex","disabled":false}]}`))
				return
			}
			if r.Method == http.MethodPatch {
				patchCalled = true
				if err := json.NewDecoder(r.Body).Decode(&patchPayload); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"status":"ok","disabled":true}`))
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case "/v0/management/api-call":
			_, _ = w.Write([]byte(`{
				"status_code": 200,
				"body": {
					"rate_limit": {
						"allowed": false,
						"primary_window": {"used_percent": 100, "limit_window_seconds": 18000},
						"secondary_window": {"used_percent": 100, "limit_window_seconds": 604800}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, true)
	createReq := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/codex-inspection/tasks",
		bytes.NewBufferString(`{
			"name": "自动禁用零额度账号",
			"dryRun": false,
			"autoAction": {
				"dryRun": false,
				"zeroQuotaAction": "disable",
				"fullQuotaAction": "none",
				"invalidAction": "disable",
				"allowDelete": false,
				"requireDeletePreview": true
			}
		}`),
	)
	createReq.Header.Set("Authorization", "Bearer management-key")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRR.Code, createRR.Body.String())
	}
	var created codexInspectionTaskResponse
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runReq := httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/tasks/"+created.Task.ID+"/runs", bytes.NewBufferString(`{}`))
	runReq.Header.Set("Authorization", "Bearer management-key")
	runRR := httptest.NewRecorder()
	handler.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runRR.Code, runRR.Body.String())
	}
	if !patchCalled {
		t.Fatal("expected auth file patch to be called")
	}
	if patchPayload.Name != "codex-zero.json" || !patchPayload.Disabled {
		t.Fatalf("patch payload = %#v", patchPayload)
	}
	var response codexInspectionRunResponse
	if err := json.Unmarshal(runRR.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if len(response.Actions) != 1 {
		t.Fatalf("len(actions) = %d, body = %s", len(response.Actions), runRR.Body.String())
	}
	action := response.Actions[0]
	if action.Action != "disable" || action.DryRun || !action.Success {
		t.Fatalf("action = %#v", action)
	}
	if !strings.Contains(string(response.Run.Summary), `"actionSuccess":1`) {
		t.Fatalf("summary = %s", response.Run.Summary)
	}
}

func TestCodexInspectionAutoDeleteBlockedByDefault(t *testing.T) {
	deleteCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"files":[{"name":"codex-invalid.json","auth_index":"codex-invalid","account":"invalid@example.com","provider":"codex","disabled":false}]}`))
				return
			}
			if r.Method == http.MethodDelete {
				deleteCalled = true
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case "/v0/management/api-call":
			_, _ = w.Write([]byte(`{"status_code": 401, "body": {"error": "unauthorized"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, true)
	createReq := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/codex-inspection/tasks",
		bytes.NewBufferString(`{
			"name": "默认阻止自动删除",
			"dryRun": false,
			"autoAction": {
				"dryRun": false,
				"invalidAction": "delete",
				"allowDelete": false,
				"requireDeletePreview": true
			}
		}`),
	)
	createReq.Header.Set("Authorization", "Bearer management-key")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRR.Code, createRR.Body.String())
	}
	var created codexInspectionTaskResponse
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runReq := httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/tasks/"+created.Task.ID+"/runs", bytes.NewBufferString(`{}`))
	runReq.Header.Set("Authorization", "Bearer management-key")
	runRR := httptest.NewRecorder()
	handler.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runRR.Code, runRR.Body.String())
	}
	if deleteCalled {
		t.Fatal("delete should be blocked when allowDelete is false")
	}
	var response codexInspectionRunResponse
	if err := json.Unmarshal(runRR.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if len(response.Actions) != 0 {
		t.Fatalf("actions = %#v", response.Actions)
	}
	if len(response.Accounts) != 1 || response.Accounts[0].RecommendedAction != "keep" {
		t.Fatalf("accounts = %#v", response.Accounts)
	}
	if !strings.Contains(response.Accounts[0].ActionReason, "自动删除未开启") {
		t.Fatalf("action reason = %q", response.Accounts[0].ActionReason)
	}
}

func TestCodexInspectionNotificationFailureDoesNotFailRun(t *testing.T) {
	var webhookHeader string
	var webhookBody string
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookHeader = r.Header.Get("X-Codex-Test")
		data, _ := io.ReadAll(r.Body)
		webhookBody = string(data)
		http.Error(w, "notify failed", http.StatusInternalServerError)
	}))
	t.Cleanup(webhook.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"name":"codex-ok.json","auth_index":"codex-ok","account":"ok@example.com","provider":"codex","disabled":false}]}`))
		case "/v0/management/api-call":
			_, _ = w.Write([]byte(`{
				"status_code": 200,
				"body": {
					"rate_limit": {
						"allowed": true,
						"primary_window": {"used_percent": 10, "limit_window_seconds": 18000},
						"secondary_window": {"used_percent": 20, "limit_window_seconds": 604800}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newTestHandler(t, upstream.URL, true)
	createReq := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/codex-inspection/tasks",
		bytes.NewBufferString(`{
			"name": "通知失败不影响巡检",
			"notification": {
				"enabled": true,
				"channels": ["webhook"],
				"trigger": "always",
				"channelConfigs": {
					"webhook": {
						"url": "`+webhook.URL+`",
						"headers": {"X-Codex-Test": "ok"}
					}
				}
			}
		}`),
	)
	createReq.Header.Set("Authorization", "Bearer management-key")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRR.Code, createRR.Body.String())
	}
	var created codexInspectionTaskResponse
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runReq := httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/tasks/"+created.Task.ID+"/runs", bytes.NewBufferString(`{}`))
	runReq.Header.Set("Authorization", "Bearer management-key")
	runRR := httptest.NewRecorder()
	handler.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runRR.Code, runRR.Body.String())
	}
	if webhookHeader != "ok" || !strings.Contains(webhookBody, `"event":"codex_inspection_run_completed"`) {
		t.Fatalf("webhook header/body = %q %s", webhookHeader, webhookBody)
	}
	var response codexInspectionRunResponse
	if err := json.Unmarshal(runRR.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if response.Run.Status != "success" {
		t.Fatalf("run status = %q, body = %s", response.Run.Status, runRR.Body.String())
	}
	if len(response.Notifications) != 1 || response.Notifications[0].Status != "failed" {
		t.Fatalf("notifications = %#v", response.Notifications)
	}
}

func TestCodexInspectionSchedulerStatusEndpoint(t *testing.T) {
	handler := newTestHandler(t, "http://example.test", true)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/codex-inspection/scheduler/status", nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("scheduler status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"status":"not_started"`) ||
		!strings.Contains(rr.Body.String(), `"running":false`) {
		t.Fatalf("scheduler status response = %s", rr.Body.String())
	}
}

func closeFloat(left float64, right float64) bool {
	return math.Abs(left-right) < 0.0000001
}
