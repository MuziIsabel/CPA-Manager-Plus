package inspection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

const (
	fiveHourWindowSeconds = 18000
	weekWindowSeconds     = 604800
)

var quotaBodyPatterns = []string{"quota exhausted", "limit reached", "payment_required"}

type Runner struct {
	client   *http.Client
	notifier *Notifier
}

type RunOptions struct {
	Trigger        string
	DryRunOverride *bool
}

type executionConfig struct {
	Concurrency int `json:"concurrency"`
	TimeoutMS   int `json:"timeoutMs"`
	Retries     int `json:"retries"`
}

type autoActionConfig struct {
	DryRun               bool   `json:"dryRun"`
	ZeroQuotaAction      string `json:"zeroQuotaAction"`
	FullQuotaAction      string `json:"fullQuotaAction"`
	InvalidAction        string `json:"invalidAction"`
	AllowDelete          bool   `json:"allowDelete"`
	RequireDeletePreview bool   `json:"requireDeletePreview"`
}

type targetScope struct {
	Type         string   `json:"type"`
	FileNames    []string `json:"fileNames"`
	AuthIndices  []string `json:"authIndices"`
	Query        string   `json:"query"`
	NoteIncludes string   `json:"noteIncludes"`
	PriorityMin  *float64 `json:"priorityMin"`
	PriorityMax  *float64 `json:"priorityMax"`
}

type codexRunSummary struct {
	Total            int  `json:"total"`
	Healthy          int  `json:"healthy"`
	ZeroQuota        int  `json:"zeroQuota"`
	FullQuota        int  `json:"fullQuota"`
	Invalid          int  `json:"invalid"`
	ProbeFailed      int  `json:"probeFailed"`
	Unknown          int  `json:"unknown"`
	KeepCount        int  `json:"keepCount"`
	DisableCount     int  `json:"disableCount"`
	EnableCount      int  `json:"enableCount"`
	DeleteCount      int  `json:"deleteCount"`
	ActionSuccess    int  `json:"actionSuccess"`
	ActionFailed     int  `json:"actionFailed"`
	ActionSkipped    int  `json:"actionSkipped"`
	DryRun           bool `json:"dryRun"`
	AutoActionDryRun bool `json:"autoActionDryRun"`
}

type authFilesPayload struct {
	Files []map[string]any `json:"files"`
}

type apiCallRequest struct {
	AuthIndex string            `json:"authIndex,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      string            `json:"data,omitempty"`
}

type apiCallResponse struct {
	StatusCodeRaw any             `json:"status_code"`
	StatusCodeAlt any             `json:"statusCode"`
	Header        map[string]any  `json:"header"`
	Headers       map[string]any  `json:"headers"`
	Body          json.RawMessage `json:"body"`
}

type codexUsagePayload struct {
	PlanType        string            `json:"plan_type"`
	PlanTypeAlt     string            `json:"planType"`
	RateLimit       *codexRateLimit   `json:"rate_limit"`
	RateLimitAlt    *codexRateLimit   `json:"rateLimit"`
	CodeReviewLimit *codexRateLimit   `json:"code_review_rate_limit"`
	Additional      []json.RawMessage `json:"additional_rate_limits"`
}

type codexRateLimit struct {
	Allowed         *bool             `json:"allowed"`
	LimitReached    *bool             `json:"limit_reached"`
	LimitReachedAlt *bool             `json:"limitReached"`
	PrimaryWindow   *codexUsageWindow `json:"primary_window"`
	PrimaryAlt      *codexUsageWindow `json:"primaryWindow"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window"`
	SecondaryAlt    *codexUsageWindow `json:"secondaryWindow"`
}

type codexUsageWindow struct {
	UsedPercent       any `json:"used_percent"`
	UsedPercentAlt    any `json:"usedPercent"`
	LimitWindow       any `json:"limit_window_seconds"`
	LimitWindowAlt    any `json:"limitWindowSeconds"`
	ResetAfterSeconds any `json:"reset_after_seconds"`
	ResetAt           any `json:"reset_at"`
}

type inspectionAccount struct {
	FileName       string
	AuthIndex      string
	AccountID      string
	DisplayAccount string
	Provider       string
	Disabled       bool
	Raw            map[string]any
}

func NewRunner() *Runner {
	client := &http.Client{Timeout: 30 * time.Second}
	return &Runner{
		client:   client,
		notifier: NewNotifier(client),
	}
}

func (r *Runner) Run(ctx context.Context, setup store.Setup, task store.CodexInspectionTask, options RunOptions, db *store.Store) (store.CodexInspectionRun, error) {
	if db == nil {
		return store.CodexInspectionRun{}, errors.New("store is required")
	}
	run, err := db.CreateCodexInspectionRun(ctx, task, options.Trigger)
	if err != nil {
		return store.CodexInspectionRun{}, err
	}

	finalStatus := "success"
	finalError := ""
	results, summary, err := r.inspect(ctx, setup, task, run, options)
	if err != nil {
		finalStatus = "failed"
		finalError = err.Error()
	} else if summary.ProbeFailed > 0 || summary.Unknown > 0 {
		finalStatus = "partial"
	}

	var actions []store.CodexInspectionActionRecord
	if err == nil {
		actions = r.executeAutoActions(ctx, setup, task, run, results, summary.DryRun)
		summary = buildSummary(results, summary.DryRun, actions)
		if summary.ActionFailed > 0 && finalStatus == "success" {
			finalStatus = "partial"
		}
	}
	if task.SaveLogs {
		if insertErr := db.InsertCodexInspectionAccountResults(ctx, results); insertErr != nil && err == nil {
			finalStatus = "failed"
			finalError = insertErr.Error()
		}
	}
	if insertErr := db.InsertCodexInspectionActionRecords(ctx, actions); insertErr != nil && err == nil && finalError == "" {
		finalStatus = "failed"
		finalError = insertErr.Error()
	}
	summaryJSON, marshalErr := json.Marshal(summary)
	if marshalErr != nil && finalError == "" {
		finalStatus = "failed"
		finalError = marshalErr.Error()
	}
	finished, finishErr := db.FinishCodexInspectionRun(ctx, run.ID, finalStatus, summaryJSON, finalError)
	if finishErr != nil {
		return run, finishErr
	}
	notificationRecords := r.notifierOrDefault().SendRunNotifications(ctx, task, finished, summary, results, actions)
	if insertErr := db.InsertCodexInspectionNotificationRecords(ctx, notificationRecords); insertErr != nil {
		// Notification persistence must not fail the inspection run itself.
	}
	if _, cleanupErr := db.CleanupCodexInspectionLogs(ctx, task.ID, task.LogRetention); cleanupErr != nil {
		// Log cleanup is best effort and must not fail the inspection run.
	}
	if err != nil {
		return finished, err
	}
	return finished, nil
}

func (r *Runner) inspect(ctx context.Context, setup store.Setup, task store.CodexInspectionTask, run store.CodexInspectionRun, options RunOptions) ([]store.CodexInspectionAccountResult, codexRunSummary, error) {
	cfg := parseExecutionConfig(task.Execution)
	actionCfg := parseAutoActionConfig(task.AutoAction)
	scope := parseTargetScope(task.TargetScope)
	dryRun := task.DryRun || actionCfg.DryRun
	if options.DryRunOverride != nil {
		dryRun = *options.DryRunOverride
	}

	files, err := r.fetchAuthFiles(ctx, setup)
	if err != nil {
		return nil, codexRunSummary{DryRun: dryRun}, err
	}
	accounts := selectInspectionAccounts(files, scope)
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].FileName != accounts[j].FileName {
			return accounts[i].FileName < accounts[j].FileName
		}
		return accounts[i].DisplayAccount < accounts[j].DisplayAccount
	})

	results := make([]store.CodexInspectionAccountResult, len(accounts))
	var cursor int
	var mu sync.Mutex
	workers := cfg.Concurrency
	if workers <= 0 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}

	worker := func() {
		for {
			mu.Lock()
			index := cursor
			cursor++
			mu.Unlock()
			if index >= len(accounts) {
				return
			}
			results[index] = r.inspectAccount(ctx, setup, task, run, accounts[index], cfg)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(accounts); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	wg.Wait()

	applyAutoActionPolicy(results, actionCfg, dryRun)
	summary := buildSummary(results, dryRun, nil)
	return results, summary, nil
}

func (r *Runner) fetchAuthFiles(ctx context.Context, setup store.Setup) ([]map[string]any, error) {
	endpoint, err := managementEndpoint(setup.CPAUpstreamURL, "/v0/management/auth-files")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	res, err := r.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return nil, fmt.Errorf("auth files request failed: %s", res.Status)
	}
	var payload authFilesPayload
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Files, nil
}

func (r *Runner) inspectAccount(ctx context.Context, setup store.Setup, task store.CodexInspectionTask, run store.CodexInspectionRun, account inspectionAccount, cfg executionConfig) store.CodexInspectionAccountResult {
	now := time.Now().UnixMilli()
	result := store.CodexInspectionAccountResult{
		RunID:          run.ID,
		TaskID:         task.ID,
		FileName:       account.FileName,
		AuthIndex:      account.AuthIndex,
		AccountID:      account.AccountID,
		DisplayAccount: account.DisplayAccount,
		Provider:       account.Provider,
		DisabledBefore: account.Disabled,
		Status:         "success",
		CreatedAtMS:    now,
	}
	if account.AuthIndex == "" {
		result.Status = "unknown"
		result.Classification = "unknown"
		result.RecommendedAction = "keep"
		result.ActionReason = "缺少 auth_index，保留账号"
		result.Error = "missing auth_index"
		return result
	}

	apiResult, err := r.callCodexUsage(ctx, setup, account, cfg)
	if err != nil {
		result.Status = "failed"
		result.Classification = "probe_failed"
		result.RecommendedAction = "keep"
		result.ActionReason = "探测异常，保留账号"
		result.Error = err.Error()
		return result
	}

	statusCode := int64(apiResult.statusCode)
	result.StatusCode = &statusCode
	result.RawResult = apiResult.raw
	result.RateLimit = apiResult.rateLimitRaw
	if apiResult.usedPercent != nil {
		value := *apiResult.usedPercent
		result.UsedPercent = &value
	}

	result.Classification, result.RecommendedAction, result.ActionReason = classifyCodexResult(account, apiResult, 100)
	if result.Classification == "probe_failed" {
		result.Status = "failed"
	} else if result.Classification == "unknown" {
		result.Status = "unknown"
	}
	return result
}

type codexAPIResult struct {
	statusCode   int
	bodyText     string
	usedPercent  *float64
	quota        bool
	rateLimit    *codexRateLimit
	rateLimitRaw json.RawMessage
	raw          json.RawMessage
}

func (r *Runner) callCodexUsage(ctx context.Context, setup store.Setup, account inspectionAccount, cfg executionConfig) (codexAPIResult, error) {
	var lastErr error
	attempts := cfg.Retries + 1
	if attempts <= 0 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		result, err := r.callCodexUsageOnce(ctx, setup, account, cfg.TimeoutMS)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return codexAPIResult{}, lastErr
}

func (r *Runner) callCodexUsageOnce(ctx context.Context, setup store.Setup, account inspectionAccount, timeoutMS int) (codexAPIResult, error) {
	endpoint, err := managementEndpoint(setup.CPAUpstreamURL, "/v0/management/api-call")
	if err != nil {
		return codexAPIResult{}, err
	}
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
	}
	if account.AccountID != "" {
		headers["Chatgpt-Account-Id"] = account.AccountID
	}
	payload := apiCallRequest{
		AuthIndex: account.AuthIndex,
		Method:    http.MethodGet,
		URL:       codexUsageURL,
		Header:    headers,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return codexAPIResult{}, err
	}

	reqCtx := ctx
	cancel := func() {}
	if timeoutMS > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return codexAPIResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.httpClient().Do(req)
	if err != nil {
		return codexAPIResult{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024))
	if err != nil {
		return codexAPIResult{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return codexAPIResult{}, fmt.Errorf("api-call failed: %s", res.Status)
	}

	var response apiCallResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return codexAPIResult{}, err
	}
	statusCode, ok := readStatusCode(response.StatusCodeRaw)
	if !ok {
		statusCode, ok = readStatusCode(response.StatusCodeAlt)
	}
	if !ok {
		return codexAPIResult{}, errors.New("api-call response missing status_code")
	}
	bodyText := normalizeAPICallBodyText(response.Body)
	payloadBody := parseCodexUsagePayload(response.Body, bodyText)
	rateLimit := payloadBody.RateLimit
	if rateLimit == nil {
		rateLimit = payloadBody.RateLimitAlt
	}
	usedPercent := deriveUsedPercent(rateLimit)
	quota := statusCode == 402 || isRateLimitReached(rateLimit)
	loweredBody := strings.ToLower(bodyText)
	for _, pattern := range quotaBodyPatterns {
		if strings.Contains(loweredBody, pattern) {
			quota = true
			break
		}
	}
	if usedPercent != nil && *usedPercent >= 100 {
		quota = true
	}

	rateLimitRaw := json.RawMessage(nil)
	if rateLimit != nil {
		if raw, err := json.Marshal(rateLimit); err == nil {
			rateLimitRaw = raw
		}
	}
	return codexAPIResult{
		statusCode:   statusCode,
		bodyText:     bodyText,
		usedPercent:  usedPercent,
		quota:        quota,
		rateLimit:    rateLimit,
		rateLimitRaw: rateLimitRaw,
		raw:          json.RawMessage(body),
	}, nil
}

func selectInspectionAccounts(files []map[string]any, scope targetScope) []inspectionAccount {
	accounts := make([]inspectionAccount, 0, len(files))
	for _, file := range files {
		account := toInspectionAccount(file)
		if account.Provider != "codex" {
			continue
		}
		if !matchesTargetScope(account, scope) {
			continue
		}
		accounts = append(accounts, account)
	}
	return accounts
}

func toInspectionAccount(file map[string]any) inspectionAccount {
	fileName := firstNonEmpty(readString(file, "name"), readString(file, "id"), normalizeAuthIndex(file["auth_index"]), normalizeAuthIndex(file["authIndex"]))
	display := firstNonEmpty(
		readString(file, "account"),
		readString(file, "email"),
		readString(file, "label"),
		fileName,
		normalizeAuthIndex(file["auth_index"]),
		normalizeAuthIndex(file["authIndex"]),
		"-",
	)
	return inspectionAccount{
		FileName:       fileName,
		AuthIndex:      firstNonEmpty(normalizeAuthIndex(file["auth_index"]), normalizeAuthIndex(file["authIndex"])),
		AccountID:      resolveCodexChatGPTAccountID(file),
		DisplayAccount: display,
		Provider:       strings.ToLower(firstNonEmpty(readString(file, "provider"), readString(file, "type"))),
		Disabled:       readDisabled(file),
		Raw:            file,
	}
}

func matchesTargetScope(account inspectionAccount, scope targetScope) bool {
	switch scope.Type {
	case "", "all_codex":
		return true
	case "files":
		wanted := stringSet(scope.FileNames)
		return wanted[account.FileName]
	case "auth_indices":
		wanted := stringSet(scope.AuthIndices)
		return wanted[account.AuthIndex]
	case "metadata_filter":
		if scope.Query != "" && !strings.Contains(strings.ToLower(metadataSearchText(account)), strings.ToLower(scope.Query)) {
			return false
		}
		if scope.NoteIncludes != "" && !strings.Contains(strings.ToLower(readString(account.Raw, "note")), strings.ToLower(scope.NoteIncludes)) {
			return false
		}
		priority := readFloat(account.Raw["priority"])
		if scope.PriorityMin != nil && (priority == nil || *priority < *scope.PriorityMin) {
			return false
		}
		if scope.PriorityMax != nil && (priority == nil || *priority > *scope.PriorityMax) {
			return false
		}
		return true
	default:
		return true
	}
}

func metadataSearchText(account inspectionAccount) string {
	values := []string{
		account.FileName,
		account.AuthIndex,
		account.AccountID,
		account.DisplayAccount,
		account.Provider,
		readString(account.Raw, "label"),
		readString(account.Raw, "email"),
		readString(account.Raw, "note"),
	}
	return strings.Join(values, " ")
}

func parseExecutionConfig(raw json.RawMessage) executionConfig {
	cfg := executionConfig{Concurrency: 4, TimeoutMS: 15000, Retries: 0}
	_ = json.Unmarshal(raw, &cfg)
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 15000
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	return cfg
}

func parseTargetScope(raw json.RawMessage) targetScope {
	scope := targetScope{Type: "all_codex"}
	_ = json.Unmarshal(raw, &scope)
	scope.Type = strings.ToLower(strings.TrimSpace(scope.Type))
	return scope
}

func parseAutoActionConfig(raw json.RawMessage) autoActionConfig {
	cfg := autoActionConfig{
		DryRun:               true,
		ZeroQuotaAction:      "disable",
		FullQuotaAction:      "disable",
		InvalidAction:        "disable",
		AllowDelete:          false,
		RequireDeletePreview: true,
	}
	var input struct {
		DryRun               *bool  `json:"dryRun"`
		ZeroQuotaAction      string `json:"zeroQuotaAction"`
		FullQuotaAction      string `json:"fullQuotaAction"`
		InvalidAction        string `json:"invalidAction"`
		AllowDelete          *bool  `json:"allowDelete"`
		RequireDeletePreview *bool  `json:"requireDeletePreview"`
	}
	if err := json.Unmarshal(raw, &input); err == nil {
		if input.DryRun != nil {
			cfg.DryRun = *input.DryRun
		}
		if input.ZeroQuotaAction != "" {
			cfg.ZeroQuotaAction = normalizeAutoAction(input.ZeroQuotaAction, false, cfg.ZeroQuotaAction)
		}
		if input.FullQuotaAction != "" {
			cfg.FullQuotaAction = normalizeAutoAction(input.FullQuotaAction, false, cfg.FullQuotaAction)
		}
		if input.InvalidAction != "" {
			cfg.InvalidAction = normalizeAutoAction(input.InvalidAction, true, cfg.InvalidAction)
		}
		if input.AllowDelete != nil {
			cfg.AllowDelete = *input.AllowDelete
		}
		if input.RequireDeletePreview != nil {
			cfg.RequireDeletePreview = *input.RequireDeletePreview
		}
	}
	cfg.ZeroQuotaAction = normalizeAutoAction(cfg.ZeroQuotaAction, false, "disable")
	cfg.FullQuotaAction = normalizeAutoAction(cfg.FullQuotaAction, false, "disable")
	cfg.InvalidAction = normalizeAutoAction(cfg.InvalidAction, true, "disable")
	return cfg
}

func normalizeAutoAction(action string, allowDelete bool, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "none", "keep", "":
		return "none"
	case "disable":
		return "disable"
	case "enable":
		return "enable"
	case "delete":
		if allowDelete {
			return "delete"
		}
	}
	return fallback
}

func applyAutoActionPolicy(results []store.CodexInspectionAccountResult, cfg autoActionConfig, dryRun bool) {
	for i := range results {
		action, reason := chooseAutoAction(results[i], cfg, dryRun)
		results[i].RecommendedAction = action
		if reason != "" {
			results[i].ActionReason = appendActionReason(results[i].ActionReason, reason)
		}
	}
}

func chooseAutoAction(result store.CodexInspectionAccountResult, cfg autoActionConfig, dryRun bool) (string, string) {
	action := result.RecommendedAction
	switch result.Classification {
	case "zero_quota":
		action = configuredActionToRecommendation(cfg.ZeroQuotaAction)
	case "full_quota":
		action = configuredActionToRecommendation(cfg.FullQuotaAction)
	case "invalid":
		action = configuredActionToRecommendation(cfg.InvalidAction)
	case "probe_failed", "unknown":
		return "keep", "探测失败或未知状态不会自动处理"
	}
	if action == "" || action == "none" {
		action = "keep"
	}
	if action == "delete" {
		if !cfg.AllowDelete {
			return "keep", "自动删除未开启，已转为保留"
		}
		if !dryRun && cfg.RequireDeletePreview {
			return "keep", "自动删除需要先通过 dry-run 预览并关闭预览保护"
		}
	}
	if action == "disable" && result.DisabledBefore {
		return "keep", "账号已禁用，无需重复禁用"
	}
	if action == "enable" && !result.DisabledBefore {
		return "keep", "账号已启用，无需重复启用"
	}
	return action, ""
}

func configuredActionToRecommendation(action string) string {
	if action == "none" {
		return "keep"
	}
	return action
}

func appendActionReason(current string, extra string) string {
	current = strings.TrimSpace(current)
	extra = strings.TrimSpace(extra)
	if current == "" {
		return extra
	}
	if extra == "" || strings.Contains(current, extra) {
		return current
	}
	return current + "；" + extra
}

func classifyCodexResult(account inspectionAccount, result codexAPIResult, threshold float64) (classification string, action string, reason string) {
	if result.statusCode == 401 {
		return "invalid", "disable", "接口返回 401，建议禁用失效账号"
	}
	if result.rateLimit != nil {
		weekly := pickWeeklyWindow(result.rateLimit)
		weeklyUsed := windowUsedPercent(weekly)
		fiveHour := pickFiveHourWindow(result.rateLimit)
		fiveHourUsed := windowUsedPercent(fiveHour)
		if weekly != nil && weeklyUsed != nil {
			if *weeklyUsed >= threshold || result.quota {
				if account.Disabled {
					return "zero_quota", "keep", "周额度达到阈值，但账号已禁用"
				}
				return "zero_quota", "disable", "周额度达到阈值，建议禁用账号"
			}
			if account.Disabled {
				return "healthy", "enable", "周额度仍可用，建议重新启用账号"
			}
			if fiveHourUsed != nil && *fiveHourUsed >= threshold {
				return "full_quota", "keep", "5 小时额度达到阈值，但周额度仍可用，暂不禁用账号"
			}
			return "healthy", "keep", "周额度仍可用，无需处理"
		}
	}
	if result.quota || (result.usedPercent != nil && *result.usedPercent >= threshold) {
		if account.Disabled {
			return "zero_quota", "keep", "额度达到阈值，但账号已禁用"
		}
		return "zero_quota", "disable", "额度达到阈值，建议禁用账号"
	}
	if result.statusCode >= 200 && result.statusCode < 300 {
		if account.Disabled {
			return "healthy", "enable", "账号恢复健康，建议重新启用"
		}
		return "healthy", "keep", "无需处理"
	}
	return "unknown", "keep", "无法安全判定账号状态，保留账号"
}

func buildSummary(results []store.CodexInspectionAccountResult, dryRun bool, actions []store.CodexInspectionActionRecord) codexRunSummary {
	summary := codexRunSummary{Total: len(results), DryRun: dryRun, AutoActionDryRun: dryRun}
	for _, result := range results {
		switch result.Classification {
		case "healthy":
			summary.Healthy++
		case "zero_quota":
			summary.ZeroQuota++
		case "full_quota":
			summary.FullQuota++
		case "invalid":
			summary.Invalid++
		case "probe_failed":
			summary.ProbeFailed++
		default:
			summary.Unknown++
		}
		switch result.RecommendedAction {
		case "disable":
			summary.DisableCount++
		case "enable":
			summary.EnableCount++
		case "delete":
			summary.DeleteCount++
		default:
			summary.KeepCount++
		}
	}
	for _, action := range actions {
		if action.Success {
			summary.ActionSuccess++
		} else if action.Error != "" {
			summary.ActionFailed++
		} else {
			summary.ActionSkipped++
		}
	}
	return summary
}

func (r *Runner) executeAutoActions(ctx context.Context, setup store.Setup, task store.CodexInspectionTask, run store.CodexInspectionRun, results []store.CodexInspectionAccountResult, dryRun bool) []store.CodexInspectionActionRecord {
	records := make([]store.CodexInspectionActionRecord, 0)
	for _, result := range results {
		action := strings.ToLower(strings.TrimSpace(result.RecommendedAction))
		if action != "disable" && action != "enable" && action != "delete" {
			continue
		}
		now := time.Now().UnixMilli()
		record := store.CodexInspectionActionRecord{
			TaskID:        task.ID,
			RunID:         run.ID,
			FileName:      result.FileName,
			AuthIndex:     result.AuthIndex,
			Action:        action,
			TriggerReason: result.ActionReason,
			BeforeState:   actionStateJSON(result, result.DisabledBefore, false),
			DryRun:        dryRun,
			CreatedAtMS:   now,
		}
		if dryRun {
			record.Success = true
			record.AfterState = record.BeforeState
			records = append(records, record)
			continue
		}

		var err error
		switch action {
		case "disable":
			err = r.setAuthFileDisabled(ctx, setup, result.FileName, true)
			record.AfterState = actionStateJSON(result, true, false)
		case "enable":
			err = r.setAuthFileDisabled(ctx, setup, result.FileName, false)
			record.AfterState = actionStateJSON(result, false, false)
		case "delete":
			err = r.deleteAuthFile(ctx, setup, result.FileName)
			record.AfterState = actionStateJSON(result, result.DisabledBefore, err == nil)
		}
		if err != nil {
			record.Error = err.Error()
		} else {
			record.Success = true
		}
		records = append(records, record)
	}
	return records
}

func (r *Runner) setAuthFileDisabled(ctx context.Context, setup store.Setup, fileName string, disabled bool) error {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return errors.New("auth file name is required")
	}
	payload := map[string]any{
		"name":     fileName,
		"disabled": disabled,
	}
	if err := r.managementJSON(ctx, setup, http.MethodPatch, "/v0/management/auth-files", payload); err == nil {
		return nil
	}
	return r.managementJSON(ctx, setup, http.MethodPatch, "/v0/management/auth-files/status", payload)
}

func (r *Runner) deleteAuthFile(ctx context.Context, setup store.Setup, fileName string) error {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return errors.New("auth file name is required")
	}
	return r.managementJSON(ctx, setup, http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
}

func (r *Runner) managementJSON(ctx context.Context, setup store.Setup, method string, path string, payload any) error {
	endpoint, err := managementEndpoint(setup.CPAUpstreamURL, path)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := r.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return nil
	}
	bodyText, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	return fmt.Errorf("management %s %s failed: %s %s", method, path, res.Status, strings.TrimSpace(string(bodyText)))
}

func actionStateJSON(result store.CodexInspectionAccountResult, disabled bool, deleted bool) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"fileName":       result.FileName,
		"authIndex":      result.AuthIndex,
		"accountId":      result.AccountID,
		"displayAccount": result.DisplayAccount,
		"provider":       result.Provider,
		"disabled":       disabled,
		"deleted":        deleted,
	})
	if err != nil {
		return nil
	}
	return data
}

func parseCodexUsagePayload(raw json.RawMessage, fallback string) codexUsagePayload {
	var payload codexUsagePayload
	if len(raw) > 0 && string(raw) != "null" {
		if raw[0] == '"' {
			var text string
			if err := json.Unmarshal(raw, &text); err == nil {
				_ = json.Unmarshal([]byte(text), &payload)
				return payload
			}
		}
		_ = json.Unmarshal(raw, &payload)
		return payload
	}
	_ = json.Unmarshal([]byte(fallback), &payload)
	return payload
}

func deriveUsedPercent(rateLimit *codexRateLimit) *float64 {
	values := make([]float64, 0, 2)
	for _, window := range limitWindows(rateLimit) {
		if value := windowUsedPercent(window); value != nil {
			values = append(values, *value)
		}
	}
	if len(values) == 0 {
		return nil
	}
	maxValue := values[0]
	for _, value := range values[1:] {
		if value > maxValue {
			maxValue = value
		}
	}
	return &maxValue
}

func isRateLimitReached(rateLimit *codexRateLimit) bool {
	if rateLimit == nil {
		return false
	}
	if rateLimit.Allowed != nil && !*rateLimit.Allowed {
		return true
	}
	if boolValue(rateLimit.LimitReached) || boolValue(rateLimit.LimitReachedAlt) {
		return true
	}
	for _, window := range limitWindows(rateLimit) {
		if value := windowUsedPercent(window); value != nil && *value >= 100 {
			return true
		}
	}
	return false
}

func limitWindows(rateLimit *codexRateLimit) []*codexUsageWindow {
	if rateLimit == nil {
		return nil
	}
	return []*codexUsageWindow{
		firstWindow(rateLimit.PrimaryWindow, rateLimit.PrimaryAlt),
		firstWindow(rateLimit.SecondaryWindow, rateLimit.SecondaryAlt),
	}
}

func pickWeeklyWindow(rateLimit *codexRateLimit) *codexUsageWindow {
	primary := firstWindow(rateLimit.PrimaryWindow, rateLimit.PrimaryAlt)
	secondary := firstWindow(rateLimit.SecondaryWindow, rateLimit.SecondaryAlt)
	for _, window := range []*codexUsageWindow{primary, secondary} {
		if windowSeconds(window) == weekWindowSeconds {
			return window
		}
	}
	return secondary
}

func pickFiveHourWindow(rateLimit *codexRateLimit) *codexUsageWindow {
	primary := firstWindow(rateLimit.PrimaryWindow, rateLimit.PrimaryAlt)
	secondary := firstWindow(rateLimit.SecondaryWindow, rateLimit.SecondaryAlt)
	for _, window := range []*codexUsageWindow{primary, secondary} {
		if windowSeconds(window) == fiveHourWindowSeconds {
			return window
		}
	}
	return primary
}

func windowUsedPercent(window *codexUsageWindow) *float64 {
	if window == nil {
		return nil
	}
	return readFloat(firstNonNil(window.UsedPercent, window.UsedPercentAlt))
}

func windowSeconds(window *codexUsageWindow) int64 {
	if window == nil {
		return 0
	}
	value := readFloat(firstNonNil(window.LimitWindow, window.LimitWindowAlt))
	if value == nil {
		return 0
	}
	return int64(*value)
}

func firstWindow(values ...*codexUsageWindow) *codexUsageWindow {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeAPICallBodyText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func readStatusCode(value any) (int, bool) {
	number := readFloat(value)
	if number == nil || math.IsNaN(*number) || math.IsInf(*number, 0) {
		return 0, false
	}
	return int(*number), true
}

func readString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return ""
}

func readDisabled(record map[string]any) bool {
	status := strings.ToLower(firstNonEmpty(readString(record, "status"), readString(record, "state")))
	if status == "disabled" || status == "inactive" {
		return true
	}
	return truthy(record["disabled"])
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "on"
	default:
		return false
	}
}

func readFloat(value any) *float64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		return &typed
	case int:
		value := float64(typed)
		return &value
	case int64:
		value := float64(typed)
		return &value
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return &parsed
		}
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%f", &parsed); err == nil {
			return &parsed
		}
	}
	return nil
}

func normalizeAuthIndex(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return ""
	}
	return text
}

func resolveCodexChatGPTAccountID(file map[string]any) string {
	for _, key := range []string{"chatgpt_account_id", "chatgptAccountId", "account_id", "accountId"} {
		if value := readString(file, key); value != "" {
			return value
		}
	}
	for _, key := range []string{"metadata", "attributes"} {
		nested, ok := file[key].(map[string]any)
		if !ok {
			continue
		}
		if id := resolveCodexChatGPTAccountID(nested); id != "" {
			return id
		}
	}
	for _, key := range []string{"id_token", "idToken"} {
		if id := extractAccountIDFromToken(file[key]); id != "" {
			return id
		}
	}
	return ""
}

func extractAccountIDFromToken(value any) string {
	if value == nil {
		return ""
	}
	if record, ok := value.(map[string]any); ok {
		return firstNonEmpty(readString(record, "chatgpt_account_id"), readString(record, "chatgptAccountId"), readString(record, "account_id"), readString(record, "accountId"))
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "{") {
		var record map[string]any
		if err := json.Unmarshal([]byte(text), &record); err == nil {
			return extractAccountIDFromToken(record)
		}
	}
	parts := strings.Split(text, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		return ""
	}
	return extractAccountIDFromToken(record)
}

func managementEndpoint(base string, path string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", errors.New("upstream URL is empty")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	parsed, err := url.Parse(base + path)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func (r *Runner) httpClient() *http.Client {
	if r != nil && r.client != nil {
		return r.client
	}
	return http.DefaultClient
}

func (r *Runner) notifierOrDefault() *Notifier {
	if r != nil && r.notifier != nil {
		return r.notifier
	}
	return NewNotifier(r.httpClient())
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
