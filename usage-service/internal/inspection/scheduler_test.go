package inspection

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

func TestNextRunAfterIntervalAndDailyTimes(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	base := time.Date(2026, 5, 12, 8, 30, 0, 0, loc)

	interval, err := NextRunAfter(json.RawMessage(`{"type":"interval","every":2,"unit":"hour"}`), base)
	if err != nil {
		t.Fatalf("interval next run: %v", err)
	}
	if !interval.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("interval next = %s, want %s", interval, base.Add(2*time.Hour))
	}

	daily, err := NextRunAfter(json.RawMessage(`{"type":"daily_times","times":["09:00","13:00","23:30"],"timezone":"Asia/Shanghai"}`), base)
	if err != nil {
		t.Fatalf("daily next run: %v", err)
	}
	wantDaily := time.Date(2026, 5, 12, 9, 0, 0, 0, loc)
	if !daily.Equal(wantDaily) {
		t.Fatalf("daily next = %s, want %s", daily, wantDaily)
	}

	late := time.Date(2026, 5, 12, 23, 31, 0, 0, loc)
	nextDay, err := NextRunAfter(json.RawMessage(`{"type":"daily_times","times":["09:00","13:00","23:30"],"timezone":"Asia/Shanghai"}`), late)
	if err != nil {
		t.Fatalf("daily next day run: %v", err)
	}
	wantNextDay := time.Date(2026, 5, 13, 9, 0, 0, 0, loc)
	if !nextDay.Equal(wantNextDay) {
		t.Fatalf("daily next day = %s, want %s", nextDay, wantNextDay)
	}

	manual, err := NextRunAfter(json.RawMessage(`{"type":"manual"}`), base)
	if err != nil {
		t.Fatalf("manual next run: %v", err)
	}
	if manual != nil {
		t.Fatalf("manual next = %s, want nil", manual)
	}

	if _, err := NextRunAfter(json.RawMessage(`{"type":"daily_times","times":["25:00"]}`), base); err == nil {
		t.Fatal("invalid daily time should fail")
	}
}

func TestSchedulerRunDueOnceExecutesScheduledTaskAndAdvancesNextRun(t *testing.T) {
	now := time.Date(2026, 5, 12, 8, 30, 0, 0, time.UTC)
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	var apiCallBody bytes.Buffer
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"name":"codex-alpha.json","auth_index":"codex-1","account":"alpha@example.com","provider":"codex"}]}`))
		case "/v0/management/api-call":
			apiCallBody.Reset()
			_, _ = apiCallBody.ReadFrom(r.Body)
			_, _ = w.Write([]byte(`{
				"status_code": 200,
				"body": {
					"rate_limit": {
						"allowed": true,
						"primary_window": {"used_percent": 10, "limit_window_seconds": 18000},
						"secondary_window": {"used_percent": 15, "limit_window_seconds": 604800}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	task, err := db.CreateCodexInspectionTask(context.Background(), store.CodexInspectionTaskInput{
		Name:     "定时巡检",
		Enabled:  boolPtr(true),
		Schedule: json.RawMessage(`{"type":"interval","every":5,"unit":"minute"}`),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	due := now.Add(-time.Second).UnixMilli()
	task, err = db.UpdateCodexInspectionTaskNextRun(context.Background(), task.ID, &due)
	if err != nil {
		t.Fatalf("set next run: %v", err)
	}

	scheduler := NewScheduler(
		db,
		NewRunner(),
		func(ctx context.Context) (store.Setup, bool, error) {
			return store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"}, true, nil
		},
		SchedulerOptions{
			Now:    func() time.Time { return now },
			Logger: func(format string, args ...any) {},
		},
	)
	launched, err := scheduler.RunDueOnce(context.Background())
	if err != nil {
		t.Fatalf("run due once: %v", err)
	}
	if launched != 1 {
		t.Fatalf("launched = %d, want 1", launched)
	}
	if !bytes.Contains(apiCallBody.Bytes(), []byte(`"authIndex":"codex-1"`)) {
		t.Fatalf("api-call body = %s", apiCallBody.String())
	}

	runs, total, err := db.ListCodexInspectionRuns(context.Background(), task.ID, "", 1, 20)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if total != 1 || len(runs) != 1 {
		t.Fatalf("runs total = %d len = %d", total, len(runs))
	}
	if runs[0].Trigger != "scheduled" || runs[0].Status != "success" {
		t.Fatalf("run = %#v", runs[0])
	}
	accounts, err := db.ListCodexInspectionAccountResults(context.Background(), runs[0].ID)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Classification != "healthy" || accounts[0].RecommendedAction != "keep" {
		t.Fatalf("accounts = %#v", accounts)
	}

	updated, err := db.GetCodexInspectionTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	wantNext := now.Add(5 * time.Minute).UnixMilli()
	if updated.NextRunAtMS == nil || *updated.NextRunAtMS != wantNext {
		t.Fatalf("next run = %v, want %d", updated.NextRunAtMS, wantNext)
	}
}

func TestSchedulerRunDueOnceSkipsInMemoryRunningTask(t *testing.T) {
	now := time.Date(2026, 5, 12, 8, 30, 0, 0, time.UTC)
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	task, err := db.CreateCodexInspectionTask(context.Background(), store.CodexInspectionTaskInput{
		Name:     "重复执行保护",
		Enabled:  boolPtr(true),
		Schedule: json.RawMessage(`{"type":"interval","every":5,"unit":"minute"}`),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	due := now.Add(-time.Second).UnixMilli()
	if _, err := db.UpdateCodexInspectionTaskNextRun(context.Background(), task.ID, &due); err != nil {
		t.Fatalf("set next run: %v", err)
	}
	scheduler := NewScheduler(
		db,
		NewRunner(),
		func(ctx context.Context) (store.Setup, bool, error) {
			t.Fatal("resolver should not be called for in-memory running task")
			return store.Setup{}, false, nil
		},
		SchedulerOptions{
			Now:    func() time.Time { return now },
			Logger: func(format string, args ...any) {},
		},
	)
	if !scheduler.markTaskRunning(task.ID) {
		t.Fatal("failed to mark task running")
	}
	defer scheduler.unmarkTaskRunning(task.ID)

	launched, err := scheduler.RunDueOnce(context.Background())
	if err != nil {
		t.Fatalf("run due once: %v", err)
	}
	if launched != 0 {
		t.Fatalf("launched = %d, want 0", launched)
	}
}

func boolPtr(value bool) *bool {
	return &value
}
