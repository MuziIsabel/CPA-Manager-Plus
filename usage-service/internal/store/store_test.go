package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager/usage-service/internal/usage"
)

func TestStorePersistsAccountSnapshot(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	_, err = db.InsertEvents(context.Background(), []usage.Event{
		{
			EventHash:            "event-1",
			TimestampMS:          1_778_000_000_000,
			Timestamp:            "2026-05-06T00:00:00Z",
			Model:                "gpt-test",
			Endpoint:             "POST /v1/chat/completions",
			AuthIndex:            "auth-1",
			APIKeyHash:           "api-key-hash-1",
			AccountSnapshot:      "alice@example.com",
			AuthLabelSnapshot:    "Alice",
			AuthFileSnapshot:     "alice.json",
			AuthProviderSnapshot: "codex",
			AuthSnapshotAtMS:     1_778_000_000_100,
			CreatedAtMS:          1_778_000_000_200,
		},
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	event := events[0]
	if event.AccountSnapshot != "alice@example.com" {
		t.Fatalf("AccountSnapshot = %q", event.AccountSnapshot)
	}
	if event.AuthLabelSnapshot != "Alice" {
		t.Fatalf("AuthLabelSnapshot = %q", event.AuthLabelSnapshot)
	}
	if event.AuthFileSnapshot != "alice.json" {
		t.Fatalf("AuthFileSnapshot = %q", event.AuthFileSnapshot)
	}
	if event.AuthProviderSnapshot != "codex" {
		t.Fatalf("AuthProviderSnapshot = %q", event.AuthProviderSnapshot)
	}
	if event.AuthSnapshotAtMS != 1_778_000_000_100 {
		t.Fatalf("AuthSnapshotAtMS = %d", event.AuthSnapshotAtMS)
	}
	if event.APIKeyHash != "api-key-hash-1" {
		t.Fatalf("APIKeyHash = %q", event.APIKeyHash)
	}

	payload := usage.BuildPayload(events)
	detail := payload.APIs["POST /v1/chat/completions"].Models["gpt-test"].Details[0]
	if detail.APIKeyHash != "api-key-hash-1" {
		t.Fatalf("payload APIKeyHash = %q", detail.APIKeyHash)
	}
	if detail.AccountSnapshot != "alice@example.com" {
		t.Fatalf("payload AccountSnapshot = %q", detail.AccountSnapshot)
	}
	if detail.AuthProviderSnapshot != "codex" {
		t.Fatalf("payload AuthProviderSnapshot = %q", detail.AuthProviderSnapshot)
	}
}

func TestStoreAPIKeyAliases(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: " Alice "},
	}); err != nil {
		t.Fatalf("upsert alias: %v", err)
	}

	aliases, err := db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load aliases: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("len(aliases) = %d, want 1", len(aliases))
	}
	if aliases[0].APIKeyHash != hash || aliases[0].Alias != "Alice" || aliases[0].UpdatedAtMS <= 0 {
		t.Fatalf("alias = %#v", aliases[0])
	}

	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: "Team A"},
	}); err != nil {
		t.Fatalf("update alias: %v", err)
	}
	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("reload aliases: %v", err)
	}
	if len(aliases) != 1 || aliases[0].Alias != "Team A" {
		t.Fatalf("updated aliases = %#v", aliases)
	}

	const otherHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: otherHash, Alias: " team a "},
	}); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("duplicate alias error = %v", err)
	}
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: "Alpha"},
		{APIKeyHash: otherHash, Alias: " alpha "},
	}); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("batch duplicate alias error = %v", err)
	}

	if err := db.DeleteAPIKeyAlias(context.Background(), hash); err != nil {
		t.Fatalf("delete alias: %v", err)
	}
	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases after delete = %#v", aliases)
	}
}

func TestCodexInspectionTaskCRUDDefaultsAndSoftDelete(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	task, err := db.CreateCodexInspectionTask(context.Background(), CodexInspectionTaskInput{
		Name: "每日巡检",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.ID == "" {
		t.Fatal("task id is empty")
	}
	if task.Name != "每日巡检" {
		t.Fatalf("task name = %q", task.Name)
	}
	if task.Enabled {
		t.Fatal("new task should be disabled by default")
	}
	if !task.SaveLogs {
		t.Fatal("new task should save logs by default")
	}
	if !task.DryRun {
		t.Fatal("new task should use dry-run by default")
	}
	if string(task.TargetScope) != defaultCodexInspectionTargetScopeJSON {
		t.Fatalf("target scope = %s", task.TargetScope)
	}

	task, err = db.SetCodexInspectionTaskEnabled(context.Background(), task.ID, true)
	if err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if !task.Enabled {
		t.Fatal("task should be enabled")
	}

	tasks, err := db.ListCodexInspectionTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}

	if err := db.DeleteCodexInspectionTask(context.Background(), task.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	tasks, err = db.ListCodexInspectionTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks after delete: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("len(tasks) after delete = %d, want 0", len(tasks))
	}
	_, err = db.GetCodexInspectionTask(context.Background(), task.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get deleted task err = %v, want sql.ErrNoRows", err)
	}
}

func TestCodexInspectionRunRejectsConcurrentTaskAndMarksStaleInterrupted(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	task, err := db.CreateCodexInspectionTask(context.Background(), CodexInspectionTaskInput{
		Name: "并发保护巡检",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := db.CreateCodexInspectionRun(context.Background(), task, "manual")
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	if run.Status != "running" {
		t.Fatalf("first run status = %q", run.Status)
	}
	if _, err := db.CreateCodexInspectionRun(context.Background(), task, "manual"); !errors.Is(err, ErrCodexInspectionTaskRunning) {
		t.Fatalf("second run err = %v, want ErrCodexInspectionTaskRunning", err)
	}

	affected, err := db.MarkStaleCodexInspectionRunsInterrupted(context.Background(), "test restart")
	if err != nil {
		t.Fatalf("mark stale interrupted: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	interruptedRun, err := db.GetCodexInspectionRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get interrupted run: %v", err)
	}
	if interruptedRun.Status != "interrupted" || interruptedRun.Error != "test restart" {
		t.Fatalf("interrupted run = %#v", interruptedRun)
	}
	interruptedTask, err := db.GetCodexInspectionTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get interrupted task: %v", err)
	}
	if interruptedTask.Status != "interrupted" || interruptedTask.LastRunStatus != "interrupted" {
		t.Fatalf("interrupted task = %#v", interruptedTask)
	}

	if _, err := db.CreateCodexInspectionRun(context.Background(), interruptedTask, "manual"); err != nil {
		t.Fatalf("create run after interrupted: %v", err)
	}
}

func TestCleanupCodexInspectionLogsByDaysDeletesAssociatedRecords(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	task, err := db.CreateCodexInspectionTask(context.Background(), CodexInspectionTaskInput{
		Name: "日志清理巡检",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	oldRun, err := db.CreateCodexInspectionRun(context.Background(), task, "manual")
	if err != nil {
		t.Fatalf("create old run: %v", err)
	}
	oldRun, err = db.FinishCodexInspectionRun(context.Background(), oldRun.ID, "success", json.RawMessage(`{"total":1}`), "")
	if err != nil {
		t.Fatalf("finish old run: %v", err)
	}
	task, err = db.GetCodexInspectionTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	newRun, err := db.CreateCodexInspectionRun(context.Background(), task, "manual")
	if err != nil {
		t.Fatalf("create new run: %v", err)
	}
	if _, err := db.FinishCodexInspectionRun(context.Background(), newRun.ID, "success", json.RawMessage(`{"total":1}`), ""); err != nil {
		t.Fatalf("finish new run: %v", err)
	}

	oldCreatedAt := oldRun.CreatedAtMS - int64(10*24*60*60*1000)
	if _, err := db.db.Exec(`update codex_inspection_runs set created_at_ms = ? where id = ?`, oldCreatedAt, oldRun.ID); err != nil {
		t.Fatalf("age old run: %v", err)
	}
	if err := db.InsertCodexInspectionAccountResults(context.Background(), []CodexInspectionAccountResult{{
		RunID:          oldRun.ID,
		TaskID:         task.ID,
		FileName:       "old.json",
		Status:         "success",
		Classification: "healthy",
		CreatedAtMS:    oldCreatedAt,
	}}); err != nil {
		t.Fatalf("insert account result: %v", err)
	}
	if err := db.InsertCodexInspectionActionRecords(context.Background(), []CodexInspectionActionRecord{{
		RunID:       oldRun.ID,
		TaskID:      task.ID,
		FileName:    "old.json",
		Action:      "disable",
		DryRun:      true,
		Success:     true,
		CreatedAtMS: oldCreatedAt,
	}}); err != nil {
		t.Fatalf("insert action record: %v", err)
	}
	if err := db.InsertCodexInspectionNotificationRecords(context.Background(), []CodexInspectionNotificationRecord{{
		RunID:       oldRun.ID,
		TaskID:      task.ID,
		Channel:     "webhook",
		Status:      "success",
		CreatedAtMS: oldCreatedAt,
	}}); err != nil {
		t.Fatalf("insert notification record: %v", err)
	}

	audit, err := db.CleanupCodexInspectionLogs(context.Background(), task.ID, json.RawMessage(`{"mode":"days","days":7}`))
	if err != nil {
		t.Fatalf("cleanup logs: %v", err)
	}
	if audit.DeletedRuns != 1 || audit.DeletedAccountResults != 1 || audit.DeletedActions != 1 || audit.DeletedNotifications != 1 {
		t.Fatalf("audit = %#v", audit)
	}
	if _, err := db.GetCodexInspectionRun(context.Background(), oldRun.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old run err = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.GetCodexInspectionRun(context.Background(), newRun.ID); err != nil {
		t.Fatalf("new run should remain: %v", err)
	}
}
