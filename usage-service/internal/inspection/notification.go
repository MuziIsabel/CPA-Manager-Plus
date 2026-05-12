package inspection

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

type Notifier struct {
	client *http.Client
}

type notificationConfig struct {
	Enabled        bool                       `json:"enabled"`
	Channels       []string                   `json:"channels"`
	Trigger        string                     `json:"trigger"`
	ChannelConfigs map[string]map[string]any  `json:"channelConfigs"`
	Headers        map[string]string          `json:"headers"`
	Extra          map[string]json.RawMessage `json:"-"`
}

type codexInspectionNotificationPayload struct {
	Event               string              `json:"event"`
	TaskID              string              `json:"taskId"`
	TaskName            string              `json:"taskName"`
	RunID               string              `json:"runId"`
	BatchID             string              `json:"batchId"`
	Trigger             string              `json:"trigger"`
	Status              string              `json:"status"`
	StartedAtMS         *int64              `json:"startedAtMs,omitempty"`
	EndedAtMS           *int64              `json:"endedAtMs,omitempty"`
	DurationMS          *int64              `json:"durationMs,omitempty"`
	Summary             codexRunSummary     `json:"summary"`
	FailedActionSummary []map[string]string `json:"failedActionSummary,omitempty"`
	ManualRequired      []map[string]string `json:"manualRequired,omitempty"`
	LogID               string              `json:"logId"`
	GeneratedAtMS       int64               `json:"generatedAtMs"`
}

func NewNotifier(client *http.Client) *Notifier {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Notifier{client: client}
}

func (n *Notifier) SendRunNotifications(
	ctx context.Context,
	task store.CodexInspectionTask,
	run store.CodexInspectionRun,
	summary codexRunSummary,
	accounts []store.CodexInspectionAccountResult,
	actions []store.CodexInspectionActionRecord,
) []store.CodexInspectionNotificationRecord {
	cfg := parseNotificationConfig(task.Notification)
	if !cfg.Enabled || len(cfg.Channels) == 0 {
		return nil
	}
	if !shouldSendNotification(cfg.Trigger, run, summary, accounts, actions) {
		return nil
	}
	payload := buildNotificationPayload(task, run, summary, accounts, actions)
	text := renderNotificationText(payload)
	records := make([]store.CodexInspectionNotificationRecord, 0, len(cfg.Channels))
	for _, channel := range uniqueChannels(cfg.Channels) {
		record := store.CodexInspectionNotificationRecord{
			TaskID:      task.ID,
			RunID:       run.ID,
			Channel:     channel,
			Status:      "success",
			CreatedAtMS: time.Now().UnixMilli(),
		}
		summaryText, err := n.sendChannel(ctx, cfg, channel, text, payload)
		if err != nil {
			record.Status = "failed"
			record.Error = err.Error()
		}
		record.ResponseSummary = summaryText
		records = append(records, record)
	}
	return records
}

func (n *Notifier) SendTestNotification(ctx context.Context, raw json.RawMessage) []store.CodexInspectionNotificationRecord {
	task := store.CodexInspectionTask{
		ID:           "test",
		Name:         "Codex 巡检通知测试",
		Notification: raw,
	}
	now := time.Now().UnixMilli()
	run := store.CodexInspectionRun{
		ID:          "test",
		BatchID:     "test",
		TaskID:      task.ID,
		Trigger:     "manual",
		Status:      "success",
		StartedAtMS: &now,
		EndedAtMS:   &now,
		CreatedAtMS: now,
	}
	summary := codexRunSummary{Total: 1, Healthy: 1, KeepCount: 1, DryRun: true, AutoActionDryRun: true}
	return n.SendRunNotifications(ctx, task, run, summary, nil, nil)
}

func parseNotificationConfig(raw json.RawMessage) notificationConfig {
	cfg := notificationConfig{
		Enabled:        false,
		Channels:       []string{},
		Trigger:        "auto_action",
		ChannelConfigs: map[string]map[string]any{},
	}
	if len(raw) == 0 {
		return cfg
	}
	_ = json.Unmarshal(raw, &cfg)
	cfg.Trigger = strings.ToLower(strings.TrimSpace(cfg.Trigger))
	if cfg.Trigger == "" {
		cfg.Trigger = "auto_action"
	}
	if cfg.ChannelConfigs == nil {
		cfg.ChannelConfigs = map[string]map[string]any{}
	}
	return cfg
}

func shouldSendNotification(trigger string, run store.CodexInspectionRun, summary codexRunSummary, accounts []store.CodexInspectionAccountResult, actions []store.CodexInspectionActionRecord) bool {
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "", "always":
		return true
	case "abnormal":
		return run.Status != "success" ||
			summary.Invalid > 0 ||
			summary.ProbeFailed > 0 ||
			summary.Unknown > 0 ||
			summary.ActionFailed > 0
	case "auto_action":
		return len(actions) > 0
	case "manual_required":
		return len(manualRequiredAccounts(accounts)) > 0
	default:
		return true
	}
}

func buildNotificationPayload(
	task store.CodexInspectionTask,
	run store.CodexInspectionRun,
	summary codexRunSummary,
	accounts []store.CodexInspectionAccountResult,
	actions []store.CodexInspectionActionRecord,
) codexInspectionNotificationPayload {
	return codexInspectionNotificationPayload{
		Event:               "codex_inspection_run_completed",
		TaskID:              task.ID,
		TaskName:            task.Name,
		RunID:               run.ID,
		BatchID:             run.BatchID,
		Trigger:             run.Trigger,
		Status:              run.Status,
		StartedAtMS:         run.StartedAtMS,
		EndedAtMS:           run.EndedAtMS,
		DurationMS:          run.DurationMS,
		Summary:             summary,
		FailedActionSummary: failedActionSummary(actions),
		ManualRequired:      manualRequiredAccounts(accounts),
		LogID:               run.ID,
		GeneratedAtMS:       time.Now().UnixMilli(),
	}
}

func renderNotificationText(payload codexInspectionNotificationPayload) string {
	return fmt.Sprintf(
		"Codex 巡检任务：%s\n执行状态：%s\n执行批次：%s\n账号总数：%d\n正常：%d，零额度：%d，满额度：%d，失效：%d，失败：%d\n自动禁用：%d，自动启用：%d，自动删除：%d\n操作成功：%d，操作失败：%d\n日志 ID：%s",
		payload.TaskName,
		payload.Status,
		payload.BatchID,
		payload.Summary.Total,
		payload.Summary.Healthy,
		payload.Summary.ZeroQuota,
		payload.Summary.FullQuota,
		payload.Summary.Invalid,
		payload.Summary.ProbeFailed,
		payload.Summary.DisableCount,
		payload.Summary.EnableCount,
		payload.Summary.DeleteCount,
		payload.Summary.ActionSuccess,
		payload.Summary.ActionFailed,
		payload.LogID,
	)
}

func (n *Notifier) sendChannel(ctx context.Context, cfg notificationConfig, channel string, text string, payload codexInspectionNotificationPayload) (string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	channelCfg := cfg.ChannelConfigs[channel]
	switch channel {
	case "webhook":
		return n.sendCustomWebhook(ctx, channelCfg, payload)
	case "telegram":
		return n.sendTelegram(ctx, channelCfg, text)
	case "feishu":
		return n.sendFeishu(ctx, channelCfg, text)
	case "wecom":
		return n.sendWeCom(ctx, channelCfg, text)
	default:
		return "", fmt.Errorf("unsupported notification channel %q", channel)
	}
}

func (n *Notifier) sendCustomWebhook(ctx context.Context, cfg map[string]any, payload codexInspectionNotificationPayload) (string, error) {
	endpoint := firstConfigString(cfg, "url", "webhookUrl")
	if endpoint == "" {
		return "", errors.New("webhook url is required")
	}
	return n.postJSON(ctx, endpoint, payload, readHeaders(cfg))
}

func (n *Notifier) sendTelegram(ctx context.Context, cfg map[string]any, text string) (string, error) {
	token := firstConfigString(cfg, "botToken", "token")
	chatID := firstConfigString(cfg, "chatId", "chatID")
	if token == "" || chatID == "" {
		return "", errors.New("telegram botToken and chatId are required")
	}
	apiBase := strings.TrimRight(firstConfigString(cfg, "apiBaseUrl"), "/")
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	endpoint := apiBase + "/bot" + token + "/sendMessage"
	return n.postJSON(ctx, endpoint, map[string]any{
		"chat_id": chatID,
		"text":    text,
	}, nil)
}

func (n *Notifier) sendFeishu(ctx context.Context, cfg map[string]any, text string) (string, error) {
	endpoint := firstConfigString(cfg, "webhookUrl", "url")
	if endpoint == "" {
		return "", errors.New("feishu webhookUrl is required")
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": text},
	}
	if secret := firstConfigString(cfg, "secret"); secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = timestamp
		payload["sign"] = signFeishu(timestamp, secret)
	}
	return n.postJSON(ctx, endpoint, payload, nil)
}

func (n *Notifier) sendWeCom(ctx context.Context, cfg map[string]any, text string) (string, error) {
	endpoint := firstConfigString(cfg, "webhookUrl", "url")
	if endpoint == "" {
		return "", errors.New("wecom webhookUrl is required")
	}
	return n.postJSON(ctx, endpoint, map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": text},
	}, nil)
}

func (n *Notifier) postJSON(ctx context.Context, endpoint string, payload any, headers map[string]string) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, value)
		}
	}
	res, err := n.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	summary := strings.TrimSpace(string(body))
	if len(summary) > 500 {
		summary = summary[:500]
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return summary, fmt.Errorf("notification request failed: %s %s", res.Status, summary)
	}
	if summary == "" {
		summary = res.Status
	}
	return summary, nil
}

func (n *Notifier) httpClient() *http.Client {
	if n != nil && n.client != nil {
		return n.client
	}
	return http.DefaultClient
}

func uniqueChannels(channels []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(channels))
	for _, channel := range channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel == "" || seen[channel] {
			continue
		}
		seen[channel] = true
		result = append(result, channel)
	}
	return result
}

func failedActionSummary(actions []store.CodexInspectionActionRecord) []map[string]string {
	result := make([]map[string]string, 0)
	for _, action := range actions {
		if action.Success {
			continue
		}
		result = append(result, map[string]string{
			"fileName":  action.FileName,
			"authIndex": action.AuthIndex,
			"action":    action.Action,
			"error":     action.Error,
		})
		if len(result) >= 10 {
			break
		}
	}
	return result
}

func manualRequiredAccounts(accounts []store.CodexInspectionAccountResult) []map[string]string {
	result := make([]map[string]string, 0)
	for _, account := range accounts {
		if account.Classification != "probe_failed" &&
			account.Classification != "unknown" &&
			!(account.RecommendedAction == "keep" && (account.Classification == "invalid" || account.Classification == "zero_quota" || account.Classification == "full_quota")) {
			continue
		}
		result = append(result, map[string]string{
			"fileName":       account.FileName,
			"authIndex":      account.AuthIndex,
			"displayAccount": account.DisplayAccount,
			"classification": account.Classification,
			"reason":         account.ActionReason,
		})
		if len(result) >= 10 {
			break
		}
	}
	return result
}

func readHeaders(cfg map[string]any) map[string]string {
	headers := map[string]string{}
	raw, ok := cfg["headers"]
	if !ok {
		return headers
	}
	switch typed := raw.(type) {
	case map[string]string:
		for key, value := range typed {
			headers[key] = value
		}
	case map[string]any:
		for key, value := range typed {
			headers[key] = fmt.Sprint(value)
		}
	}
	return headers
}

func firstConfigString(cfg map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := cfg[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func signFeishu(timestamp string, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	_, _ = mac.Write([]byte{})
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
