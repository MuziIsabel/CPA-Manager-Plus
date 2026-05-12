import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChartLine,
  IconExternalLink,
  IconEye,
  IconFileText,
  IconFilter,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconShield,
  IconTimer,
  IconTrash2,
  IconMoreVertical,
} from '@/components/ui/icons';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore, useUsageServiceStore } from '@/stores';
import type {
  CodexInspectionActionRecord,
  CodexInspectionAutoAction,
  CodexInspectionLogRetentionConfig,
  CodexInspectionNotificationChannel,
  CodexInspectionNotificationConfig,
  CodexInspectionNotificationRecord,
  CodexInspectionNotificationTrigger,
  CodexInspectionRun,
  CodexInspectionRunResponse,
  CodexInspectionScheduleConfig,
  CodexInspectionSchedulerStatus,
  CodexInspectionTargetScope,
  CodexInspectionTask,
  CodexInspectionTaskPayload,
} from '@/types/codexInspectionTask';
import { detectApiBaseFromLocation } from '@/utils/connection';
import styles from './CodexInspectionTasksPage.module.scss';

type TaskDraft = {
  name: string;
  description: string;
  enabled: boolean;
  targetType: CodexInspectionTargetScope['type'];
  fileNames: string;
  authIndices: string;
  query: string;
  noteIncludes: string;
  scheduleType: CodexInspectionScheduleConfig['type'];
  intervalEvery: string;
  intervalUnit: 'minute' | 'hour' | 'day';
  dailyTimes: string;
  timezone: string;
  concurrency: string;
  timeoutMs: string;
  retries: string;
  saveLogs: boolean;
  retentionMode: CodexInspectionLogRetentionConfig['mode'];
  retentionDays: string;
  retentionCount: string;
  dryRun: boolean;
  zeroQuotaAction: Exclude<CodexInspectionAutoAction, 'delete'>;
  fullQuotaAction: Exclude<CodexInspectionAutoAction, 'delete'>;
  invalidAction: CodexInspectionAutoAction;
  allowDelete: boolean;
  requireDeletePreview: boolean;
  notificationEnabled: boolean;
  notificationTrigger: CodexInspectionNotificationTrigger;
  notificationChannels: CodexInspectionNotificationChannel[];
  telegramBotToken: string;
  telegramChatId: string;
  feishuWebhookUrl: string;
  feishuSecret: string;
  wecomWebhookUrl: string;
  webhookUrl: string;
  webhookHeaders: string;
};

type ModalMode = 'create' | 'edit';
type DetailTab = 'overview' | 'schedule' | 'scope' | 'policy' | 'notification' | 'logs';
type TaskStatusFilter = 'all' | 'enabled' | 'disabled' | 'running' | 'warning' | 'failed';
type ScheduleFilter = 'all' | CodexInspectionScheduleConfig['type'];
type ScopeFilter = 'all' | CodexInspectionTargetScope['type'];

const DETAIL_TABS: Array<{ id: DetailTab; label: string }> = [
  { id: 'overview', label: '概览' },
  { id: 'schedule', label: '执行计划' },
  { id: 'scope', label: '巡检范围' },
  { id: 'policy', label: '自动策略' },
  { id: 'notification', label: '通知策略' },
  { id: 'logs', label: '日志记录' },
];

const DEFAULT_DRAFT: TaskDraft = {
  name: '',
  description: '',
  enabled: false,
  targetType: 'all_codex',
  fileNames: '',
  authIndices: '',
  query: '',
  noteIncludes: '',
  scheduleType: 'manual',
  intervalEvery: '6',
  intervalUnit: 'hour',
  dailyTimes: '09:00,13:00,23:30',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '',
  concurrency: '4',
  timeoutMs: '15000',
  retries: '0',
  saveLogs: true,
  retentionMode: 'days',
  retentionDays: '30',
  retentionCount: '100',
  dryRun: true,
  zeroQuotaAction: 'disable',
  fullQuotaAction: 'disable',
  invalidAction: 'disable',
  allowDelete: false,
  requireDeletePreview: true,
  notificationEnabled: false,
  notificationTrigger: 'auto_action',
  notificationChannels: ['webhook'],
  telegramBotToken: '',
  telegramChatId: '',
  feishuWebhookUrl: '',
  feishuSecret: '',
  wecomWebhookUrl: '',
  webhookUrl: '',
  webhookHeaders: '',
};

const splitList = (value: string) =>
  value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

const toNumber = (value: string, fallback: number, min = 0) => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < min) return fallback;
  return Math.floor(parsed);
};

const formatDateTime = (value?: number) => {
  if (!value) return '--';
  return new Date(value).toLocaleString();
};

const formatDuration = (value?: number) => {
  if (!value) return '--';
  if (value < 1000) return `${value} ms`;
  return `${(value / 1000).toFixed(1)} s`;
};

const summaryNumber = (run: CodexInspectionRun | null | undefined, key: string) => {
  const value = run?.summary?.[key];
  return typeof value === 'number' ? value : 0;
};

const statusTone = (status?: string) => {
  if (status === 'success') return styles.toneGood;
  if (status === 'partial' || status === 'missed') return styles.toneWarn;
  if (status === 'failed' || status === 'interrupted') return styles.toneBad;
  if (status === 'running' || status === 'queued') return styles.toneInfo;
  return styles.toneMuted;
};

const statusLabel = (status?: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'success':
      return '成功';
    case 'partial':
      return '部分异常';
    case 'failed':
      return '失败';
    case 'interrupted':
      return '已中断';
    case 'queued':
      return '排队中';
    case 'idle':
    default:
      return '空闲';
  }
};

const scheduleLabel = (schedule: CodexInspectionScheduleConfig) => {
  if (schedule.type === 'interval') {
    const unitLabel = schedule.unit === 'day' ? '天' : schedule.unit === 'hour' ? '小时' : '分钟';
    return `每 ${schedule.every} ${unitLabel}`;
  }
  if (schedule.type === 'daily_times') {
    return `每天 ${schedule.times.join('、')}`;
  }
  return '手动执行';
};

const scheduleTypeLabel = (schedule: CodexInspectionScheduleConfig) => {
  if (schedule.type === 'interval') return '固定频率';
  if (schedule.type === 'daily_times') return '指定时间';
  return '手动';
};

const scopeLabel = (scope: CodexInspectionTargetScope) => {
  if (scope.type === 'all_codex') return '全部 Codex 账号';
  if (scope.type === 'files') return `指定文件 ${scope.fileNames.length}`;
  if (scope.type === 'auth_indices') return `指定账号 ${scope.authIndices.length}`;
  return '标签/关键字筛选';
};

const actionLabel = (action: CodexInspectionAutoAction) => {
  switch (action) {
    case 'disable':
      return '禁用';
    case 'enable':
      return '启用';
    case 'delete':
      return '删除';
    case 'none':
    default:
      return '不处理';
  }
};

const notificationTriggerLabel = (trigger: CodexInspectionNotificationTrigger) => {
  switch (trigger) {
    case 'always':
      return '每次巡检';
    case 'abnormal':
      return '仅异常';
    case 'auto_action':
      return '仅有自动操作';
    case 'manual_required':
      return '仅需人工处理';
    default:
      return trigger;
  }
};

const targetSearchText = (task: CodexInspectionTask) =>
  [task.id, task.name, task.description, scheduleLabel(task.schedule), scopeLabel(task.targetScope)]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();

const parseHeaders = (value: string): Record<string, string> => {
  const headers: Record<string, string> = {};
  value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .forEach((line) => {
      const separator = line.indexOf(':');
      if (separator <= 0) return;
      const key = line.slice(0, separator).trim();
      const headerValue = line.slice(separator + 1).trim();
      if (key && headerValue) headers[key] = headerValue;
    });
  return headers;
};

const draftFromTask = (task: CodexInspectionTask): TaskDraft => {
  const schedule = task.schedule;
  const target = task.targetScope;
  const notification = task.notification;
  const webhookConfig =
    notification.channelConfigs?.webhook ??
    notification.channelConfigs?.custom ??
    {};
  const telegramConfig = notification.channelConfigs?.telegram ?? {};
  const feishuConfig = notification.channelConfigs?.feishu ?? {};
  const wecomConfig = notification.channelConfigs?.wecom ?? {};
  const headers = webhookConfig.headers;
  const headerText =
    headers && typeof headers === 'object'
      ? Object.entries(headers as Record<string, unknown>)
          .map(([key, value]) => `${key}: ${String(value)}`)
          .join('\n')
      : '';

  return {
    ...DEFAULT_DRAFT,
    name: task.name,
    description: task.description ?? '',
    enabled: task.enabled,
    targetType: target.type,
    fileNames: target.type === 'files' ? target.fileNames.join('\n') : '',
    authIndices: target.type === 'auth_indices' ? target.authIndices.join('\n') : '',
    query: target.type === 'metadata_filter' ? target.query ?? '' : '',
    noteIncludes: target.type === 'metadata_filter' ? target.noteIncludes ?? '' : '',
    scheduleType: schedule.type,
    intervalEvery: schedule.type === 'interval' ? String(schedule.every) : DEFAULT_DRAFT.intervalEvery,
    intervalUnit: schedule.type === 'interval' ? schedule.unit : DEFAULT_DRAFT.intervalUnit,
    dailyTimes: schedule.type === 'daily_times' ? schedule.times.join(',') : DEFAULT_DRAFT.dailyTimes,
    timezone:
      (schedule.type === 'interval' || schedule.type === 'daily_times' ? schedule.timezone : '') ??
      DEFAULT_DRAFT.timezone,
    concurrency: String(task.execution.concurrency),
    timeoutMs: String(task.execution.timeoutMs),
    retries: String(task.execution.retries),
    saveLogs: task.saveLogs,
    retentionMode: task.logRetention.mode,
    retentionDays: task.logRetention.mode === 'days' ? String(task.logRetention.days) : DEFAULT_DRAFT.retentionDays,
    retentionCount:
      task.logRetention.mode === 'latest' ? String(task.logRetention.count) : DEFAULT_DRAFT.retentionCount,
    dryRun: task.dryRun,
    zeroQuotaAction: task.autoAction.zeroQuotaAction,
    fullQuotaAction: task.autoAction.fullQuotaAction,
    invalidAction: task.autoAction.invalidAction,
    allowDelete: task.autoAction.allowDelete,
    requireDeletePreview: task.autoAction.requireDeletePreview,
    notificationEnabled: notification.enabled,
    notificationTrigger: notification.trigger,
    notificationChannels: notification.channels.length ? notification.channels : ['webhook'],
    telegramBotToken: String(telegramConfig.botToken ?? telegramConfig.token ?? ''),
    telegramChatId: String(telegramConfig.chatId ?? telegramConfig.chatID ?? ''),
    feishuWebhookUrl: String(feishuConfig.webhookUrl ?? feishuConfig.url ?? ''),
    feishuSecret: String(feishuConfig.secret ?? ''),
    wecomWebhookUrl: String(wecomConfig.webhookUrl ?? wecomConfig.url ?? ''),
    webhookUrl: String(webhookConfig.url ?? webhookConfig.webhookUrl ?? ''),
    webhookHeaders: headerText,
  };
};

const buildTaskPayload = (draft: TaskDraft): CodexInspectionTaskPayload => {
  let targetScope: CodexInspectionTargetScope = { type: 'all_codex' };
  if (draft.targetType === 'files') {
    targetScope = { type: 'files', fileNames: splitList(draft.fileNames) };
  } else if (draft.targetType === 'auth_indices') {
    targetScope = { type: 'auth_indices', authIndices: splitList(draft.authIndices) };
  } else if (draft.targetType === 'metadata_filter') {
    targetScope = {
      type: 'metadata_filter',
      query: draft.query.trim(),
      noteIncludes: draft.noteIncludes.trim(),
    };
  }

  let schedule: CodexInspectionScheduleConfig = { type: 'manual' };
  if (draft.scheduleType === 'interval') {
    schedule = {
      type: 'interval',
      every: toNumber(draft.intervalEvery, 6, 1),
      unit: draft.intervalUnit,
      timezone: draft.timezone.trim() || undefined,
    };
  } else if (draft.scheduleType === 'daily_times') {
    schedule = {
      type: 'daily_times',
      times: splitList(draft.dailyTimes),
      timezone: draft.timezone.trim() || undefined,
    };
  }

  let logRetention: CodexInspectionLogRetentionConfig = { mode: 'none' };
  if (draft.retentionMode === 'days') {
    logRetention = { mode: 'days', days: toNumber(draft.retentionDays, 30, 1) };
  } else if (draft.retentionMode === 'latest') {
    logRetention = { mode: 'latest', count: toNumber(draft.retentionCount, 100, 1) };
  }

  const channelConfigs: CodexInspectionNotificationConfig['channelConfigs'] = {};
  if (draft.telegramBotToken.trim() || draft.telegramChatId.trim()) {
    channelConfigs.telegram = {
      botToken: draft.telegramBotToken.trim(),
      chatId: draft.telegramChatId.trim(),
    };
  }
  if (draft.feishuWebhookUrl.trim()) {
    channelConfigs.feishu = {
      webhookUrl: draft.feishuWebhookUrl.trim(),
      secret: draft.feishuSecret.trim(),
    };
  }
  if (draft.wecomWebhookUrl.trim()) {
    channelConfigs.wecom = {
      webhookUrl: draft.wecomWebhookUrl.trim(),
    };
  }
  if (draft.webhookUrl.trim()) {
    channelConfigs.webhook = {
      url: draft.webhookUrl.trim(),
      headers: parseHeaders(draft.webhookHeaders),
    };
  }

  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    enabled: draft.enabled,
    targetScope,
    schedule,
    execution: {
      concurrency: toNumber(draft.concurrency, 4, 1),
      timeoutMs: toNumber(draft.timeoutMs, 15000, 1000),
      retries: toNumber(draft.retries, 0, 0),
    },
    saveLogs: draft.saveLogs,
    logRetention,
    dryRun: draft.dryRun,
    autoAction: {
      dryRun: draft.dryRun,
      zeroQuotaAction: draft.zeroQuotaAction,
      fullQuotaAction: draft.fullQuotaAction,
      invalidAction: draft.invalidAction,
      allowDelete: draft.allowDelete,
      requireDeletePreview: draft.requireDeletePreview,
    },
    notification: {
      enabled: draft.notificationEnabled,
      trigger: draft.notificationTrigger,
      channels: draft.notificationEnabled ? draft.notificationChannels : [],
      channelConfigs,
    },
  };
};

export function CodexInspectionTasksPage() {
  const navigate = useNavigate();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [serviceBase, setServiceBase] = useState('');
  const [tasks, setTasks] = useState<CodexInspectionTask[]>([]);
  const [runs, setRuns] = useState<CodexInspectionRun[]>([]);
  const [schedulerStatus, setSchedulerStatus] = useState<CodexInspectionSchedulerStatus | null>(null);
  const [selectedTaskId, setSelectedTaskId] = useState('');
  const [selectedRunDetail, setSelectedRunDetail] = useState<CodexInspectionRunResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [runningTaskIds, setRunningTaskIds] = useState<Set<string>>(() => new Set());
  const [modalMode, setModalMode] = useState<ModalMode>('create');
  const [taskModalOpen, setTaskModalOpen] = useState(false);
  const [wizardStep, setWizardStep] = useState(0);
  const [draft, setDraft] = useState<TaskDraft>(DEFAULT_DRAFT);
  const [runDetailOpen, setRunDetailOpen] = useState(false);
  const [detailTab, setDetailTab] = useState<DetailTab>('overview');
  const [keywordFilter, setKeywordFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<TaskStatusFilter>('all');
  const [scheduleFilter, setScheduleFilter] = useState<ScheduleFilter>('all');
  const [scopeFilter, setScopeFilter] = useState<ScopeFilter>('all');
  const [menuTaskId, setMenuTaskId] = useState('');
  const [notificationModalOpen, setNotificationModalOpen] = useState(false);

  const resolveServiceBase = useCallback(async () => {
    if (usageServiceEnabled && usageServiceBase) {
      return usageServiceBase;
    }
    const candidates = Array.from(
      new Set(
        [apiBase, detectApiBaseFromLocation()]
          .map((value) => normalizeUsageServiceBase(value || ''))
          .filter(Boolean)
      )
    );
    for (const candidate of candidates) {
      try {
        const info = await usageServiceApi.getInfo(candidate);
        if (isUsageServiceId(info.service)) return candidate;
      } catch {
        // CPA 主服务未启用 Usage Service 时会走到这里。
      }
    }
    return '';
  }, [apiBase, usageServiceBase, usageServiceEnabled]);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const base = await resolveServiceBase();
      setServiceBase(base);
      if (!base) {
        setTasks([]);
        setRuns([]);
        setSchedulerStatus(null);
        return;
      }
      const [taskResponse, runResponse, schedulerResponse] = await Promise.all([
        usageServiceApi.getCodexInspectionTasks(base, managementKey),
        usageServiceApi.getCodexInspectionRuns(base, { page: 1, pageSize: 20 }, managementKey),
        usageServiceApi.getCodexInspectionSchedulerStatus(base, managementKey),
      ]);
      setTasks(taskResponse.tasks ?? []);
      setRuns(runResponse.runs ?? []);
      setSchedulerStatus(schedulerResponse);
      setSelectedTaskId((current) => current || taskResponse.tasks?.[0]?.id || '');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setLoading(false);
    }
  }, [managementKey, resolveServiceBase, showNotification]);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  const selectedTask = useMemo(
    () => tasks.find((task) => task.id === selectedTaskId) ?? tasks[0] ?? null,
    [selectedTaskId, tasks]
  );

  const selectedTaskRuns = useMemo(
    () => runs.filter((run) => !selectedTask || run.taskId === selectedTask.id),
    [runs, selectedTask]
  );

  const filteredTasks = useMemo(() => {
    const normalizedKeyword = keywordFilter.trim().toLowerCase();
    return tasks.filter((task) => {
      const currentStatus = task.lastRunStatus ?? task.status;
      if (normalizedKeyword && !targetSearchText(task).includes(normalizedKeyword)) return false;
      if (statusFilter === 'enabled' && !task.enabled) return false;
      if (statusFilter === 'disabled' && task.enabled) return false;
      if (statusFilter === 'running' && currentStatus !== 'running' && !runningTaskIds.has(task.id)) return false;
      if (statusFilter === 'failed' && currentStatus !== 'failed' && currentStatus !== 'interrupted') return false;
      if (statusFilter === 'warning' && currentStatus !== 'partial' && currentStatus !== 'missed') return false;
      if (scheduleFilter !== 'all' && task.schedule.type !== scheduleFilter) return false;
      if (scopeFilter !== 'all' && task.targetScope.type !== scopeFilter) return false;
      return true;
    });
  }, [keywordFilter, runningTaskIds, scheduleFilter, scopeFilter, statusFilter, tasks]);

  const stats = useMemo(
    () => ({
      total: tasks.length,
      enabled: tasks.filter((task) => task.enabled).length,
      running: tasks.filter((task) => task.status === 'running').length + runningTaskIds.size,
      needAttention: runs.reduce(
        (total, run) =>
          total +
          summaryNumber(run, 'zeroQuota') +
          summaryNumber(run, 'fullQuota') +
          summaryNumber(run, 'invalid') +
          summaryNumber(run, 'probeFailed'),
        0
      ),
      notifications: tasks.filter((task) => task.notification.enabled).length,
    }),
    [runningTaskIds.size, runs, tasks]
  );

  const openCreateModal = () => {
    setModalMode('create');
    setDraft({ ...DEFAULT_DRAFT });
    setWizardStep(0);
    setTaskModalOpen(true);
  };

  const openEditModal = (task: CodexInspectionTask) => {
    setModalMode('edit');
    setDraft(draftFromTask(task));
    setWizardStep(0);
    setSelectedTaskId(task.id);
    setTaskModalOpen(true);
  };

  const saveTask = async () => {
    if (!serviceBase) return;
    const payload = buildTaskPayload(draft);
    if (!payload.name) {
      showNotification('任务名称不能为空', 'error');
      return;
    }
    setSaving(true);
    try {
      if (modalMode === 'edit' && selectedTask) {
        await usageServiceApi.updateCodexInspectionTask(serviceBase, selectedTask.id, payload, managementKey);
      } else {
        const response = await usageServiceApi.createCodexInspectionTask(serviceBase, payload, managementKey);
        setSelectedTaskId(response.task.id);
      }
      setTaskModalOpen(false);
      await loadData();
      showNotification('巡检任务已保存', 'success');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setSaving(false);
    }
  };

  const setTaskEnabled = async (task: CodexInspectionTask, enabled: boolean) => {
    if (!serviceBase) return;
    try {
      await usageServiceApi.setCodexInspectionTaskEnabled(serviceBase, task.id, enabled, managementKey);
      await loadData();
      showNotification(enabled ? '任务已启用' : '任务已停用', 'success');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    }
  };

  const runTask = async (task: CodexInspectionTask) => {
    if (!serviceBase || runningTaskIds.has(task.id)) return;
    setRunningTaskIds((previous) => new Set(previous).add(task.id));
    try {
      const detail = await usageServiceApi.runCodexInspectionTask(serviceBase, task.id, {}, managementKey);
      setSelectedRunDetail(detail);
      setRunDetailOpen(true);
      await loadData();
      showNotification('巡检执行完成', detail.run.status === 'success' ? 'success' : 'warning');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setRunningTaskIds((previous) => {
        const next = new Set(previous);
        next.delete(task.id);
        return next;
      });
    }
  };

  const deleteTask = (task: CodexInspectionTask) => {
    if (!serviceBase) return;
    showConfirmation({
      title: '删除巡检任务',
      message: `确认删除「${task.name}」？任务历史日志会保留到清理策略处理。`,
      confirmText: '删除',
      variant: 'danger',
      onConfirm: async () => {
        await usageServiceApi.deleteCodexInspectionTask(serviceBase, task.id, managementKey);
        if (selectedTaskId === task.id) setSelectedTaskId('');
        await loadData();
        showNotification('巡检任务已删除', 'success');
      },
    });
  };

  const openRunDetail = (run: CodexInspectionRun) => {
    navigate(`/monitoring/codex-inspection-tasks/runs/${encodeURIComponent(run.id)}`);
  };

  const updateDraft = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => {
    setDraft((previous) => ({ ...previous, [key]: value }));
  };

  const toggleNotificationChannel = (channel: CodexInspectionNotificationChannel) => {
    setDraft((previous) => {
      const exists = previous.notificationChannels.includes(channel);
      return {
        ...previous,
        notificationChannels: exists
          ? previous.notificationChannels.filter((item) => item !== channel)
          : [...previous.notificationChannels, channel],
      };
    });
  };

  const selectedRunActions = selectedRunDetail?.actions ?? [];
  const selectedRunNotifications = selectedRunDetail?.notifications ?? [];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <p className={styles.eyebrow}>Codex Account Inspection</p>
          <h1>Codex 巡检任务</h1>
          <p>创建、调度、自动处理并审计 Codex 账号巡检结果。</p>
        </div>
        <div className={styles.headerActions}>
          <Link to="/monitoring/codex-inspection" className={styles.secondaryLink}>
            <IconChartLine size={16} />
            <span>手动巡检</span>
            <IconExternalLink size={14} />
          </Link>
          <Button variant="secondary" onClick={loadData} loading={loading}>
            <IconRefreshCw size={16} />
            刷新
          </Button>
          <Button variant="secondary" onClick={() => setNotificationModalOpen(true)}>
            <IconSettings size={16} />
            通知配置
          </Button>
          <Button onClick={openCreateModal}>
            <IconTimer size={16} />
            新建任务
          </Button>
        </div>
      </header>

      {!serviceBase && !loading ? (
        <Card className={styles.notice}>
          <IconFileText size={20} />
          <div>
            <strong>Usage Service 未连接</strong>
            <p>Codex 巡检任务由 Usage Service 调度执行。请先在请求监控中启用 Usage Service。</p>
          </div>
        </Card>
      ) : null}

      <section className={styles.statsGrid}>
        <MetricCard label="任务总数" value={String(stats.total)} meta="全部 Codex 巡检任务" />
        <MetricCard label="已启用" value={String(stats.enabled)} meta="会被服务端调度器扫描" tone="good" />
        <MetricCard label="运行中" value={String(stats.running)} meta="包含手动触发中的任务" tone="info" />
        <MetricCard label="待处理账号" value={String(stats.needAttention)} meta="最近日志中的异常和建议" tone="warn" />
        <MetricCard label="通知任务" value={String(stats.notifications)} meta="已启用通知策略" tone="info" />
        <MetricCard
          label="调度器"
          value={schedulerStatus?.running ? '运行中' : '未启动'}
          meta={`tick ${schedulerStatus?.tickIntervalMs ?? 0} ms`}
          tone={schedulerStatus?.running ? 'good' : 'warn'}
        />
      </section>

      <section className={styles.workspace}>
        <Card className={styles.taskListPanel}>
          <div className={styles.panelHeader}>
            <div>
              <h2>任务列表</h2>
              <p>按状态、频率和范围筛选，选择任务查看右侧详情。</p>
            </div>
            <span className={styles.panelMeta}>{filteredTasks.length} / {tasks.length}</span>
          </div>
          <div className={styles.taskFilters}>
            <Input
              label="搜索"
              value={keywordFilter}
              onChange={(event) => setKeywordFilter(event.target.value)}
              placeholder="任务名 / ID / 描述"
              rightElement={<IconSearch size={15} />}
            />
            <label className={styles.field}>
              <span>状态</span>
              <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as TaskStatusFilter)}>
                <option value="all">全部</option>
                <option value="enabled">启用</option>
                <option value="disabled">停用</option>
                <option value="running">运行中</option>
                <option value="warning">需处理</option>
                <option value="failed">失败</option>
              </select>
            </label>
            <label className={styles.field}>
              <span>频率</span>
              <select value={scheduleFilter} onChange={(event) => setScheduleFilter(event.target.value as ScheduleFilter)}>
                <option value="all">全部</option>
                <option value="manual">手动</option>
                <option value="interval">固定频率</option>
                <option value="daily_times">指定时间</option>
              </select>
            </label>
            <label className={styles.field}>
              <span>范围</span>
              <select value={scopeFilter} onChange={(event) => setScopeFilter(event.target.value as ScopeFilter)}>
                <option value="all">全部</option>
                <option value="all_codex">全部账号</option>
                <option value="files">认证文件</option>
                <option value="auth_indices">指定账号</option>
                <option value="metadata_filter">标签/关键字</option>
              </select>
            </label>
          </div>
          <div className={styles.taskList}>
            <div className={styles.taskTableHeader}>
              <span>任务</span>
              <span>状态</span>
              <span>频率</span>
              <span>范围</span>
              <span>最近执行</span>
              <span>下次执行</span>
              <span />
            </div>
            {filteredTasks.map((task) => (
              <div
                key={task.id}
                role="button"
                tabIndex={0}
                className={`${styles.taskRow} ${selectedTask?.id === task.id ? styles.taskRowActive : ''}`}
                onClick={() => {
                  setSelectedTaskId(task.id);
                  setDetailTab('overview');
                  setMenuTaskId('');
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    setSelectedTaskId(task.id);
                    setDetailTab('overview');
                    setMenuTaskId('');
                  }
                }}
              >
                <span className={styles.taskRowMain}>
                  <strong>{task.name}</strong>
                  <small>{task.description || task.id}</small>
                </span>
                <span className={styles.taskStatusStack}>
                  <span className={`${styles.statusPill} ${task.enabled ? styles.pillGood : styles.pillMuted}`}>
                    {task.enabled ? '启用' : '停用'}
                  </span>
                  <span className={`${styles.runStatus} ${statusTone(task.lastRunStatus ?? task.status)}`}>
                    {statusLabel(task.lastRunStatus ?? task.status)}
                  </span>
                </span>
                <span>{scheduleTypeLabel(task.schedule)}</span>
                <span>{scopeLabel(task.targetScope)}</span>
                <span>{formatDateTime(task.lastRunAtMs)}</span>
                <span>{formatDateTime(task.nextRunAtMs)}</span>
                <span className={styles.taskRowActions}>
                  <button
                    type="button"
                    title="立即运行"
                    disabled={runningTaskIds.has(task.id)}
                    onClick={(event) => {
                      event.stopPropagation();
                      void runTask(task);
                    }}
                  >
                    <IconTimer size={15} />
                  </button>
                  <button
                    type="button"
                    title="更多"
                    onClick={(event) => {
                      event.stopPropagation();
                      setMenuTaskId((current) => (current === task.id ? '' : task.id));
                    }}
                  >
                    <IconMoreVertical size={15} />
                  </button>
                  {menuTaskId === task.id ? (
                    <span className={styles.moreMenu}>
                      <button type="button" onClick={(event) => { event.stopPropagation(); openEditModal(task); }}>编辑任务</button>
                      <button type="button" onClick={(event) => { event.stopPropagation(); void runTask(task); }}>立即运行</button>
                      <button type="button" onClick={(event) => { event.stopPropagation(); void setTaskEnabled(task, !task.enabled); }}>
                        {task.enabled ? '停用任务' : '启用任务'}
                      </button>
                      <button type="button" onClick={(event) => { event.stopPropagation(); setDetailTab('logs'); setSelectedTaskId(task.id); }}>查看日志</button>
                      <button type="button" className={styles.dangerMenuItem} onClick={(event) => { event.stopPropagation(); deleteTask(task); }}>删除任务</button>
                    </span>
                  ) : null}
                </span>
              </div>
            ))}
            {filteredTasks.length === 0 ? (
              <div className={styles.emptyState}>
                <IconFilter size={24} />
                <p>{tasks.length === 0 ? '还没有巡检任务。' : '没有匹配的巡检任务。'}</p>
                {tasks.length === 0 ? <Button size="sm" onClick={openCreateModal}>新建任务</Button> : null}
              </div>
            ) : null}
          </div>
        </Card>

        <aside className={styles.detailPanel}>
          {selectedTask ? (
            <>
              <div className={styles.detailHeader}>
                <div>
                  <span className={`${styles.statusPill} ${selectedTask.enabled ? styles.pillGood : styles.pillMuted}`}>
                    {selectedTask.enabled ? '已启用' : '已停用'}
                  </span>
                  <h2>{selectedTask.name}</h2>
                  <p>{selectedTask.description || '未填写描述'}</p>
                </div>
                <div className={styles.detailActions}>
                  <button type="button" title="编辑" onClick={() => openEditModal(selectedTask)}>
                    <IconSettings size={16} />
                  </button>
                  <button type="button" title="手动运行" onClick={() => runTask(selectedTask)}>
                    <IconTimer size={16} />
                  </button>
                  <button type="button" title="删除" onClick={() => deleteTask(selectedTask)}>
                    <IconTrash2 size={16} />
                  </button>
                </div>
              </div>

              {runningTaskIds.has(selectedTask.id) ? (
                <div className={styles.runningCard}>
                  <span className={styles.spinner} />
                  <div>
                    <strong>巡检执行中</strong>
                    <p>正在调用 CPA Management API 探测 Codex 账号。</p>
                  </div>
                </div>
              ) : null}

              <div className={styles.detailTabs}>
                {DETAIL_TABS.map((tab) => (
                  <button
                    key={tab.id}
                    type="button"
                    className={detailTab === tab.id ? styles.detailTabActive : ''}
                    onClick={() => setDetailTab(tab.id)}
                  >
                    {tab.label}
                  </button>
                ))}
              </div>

              {detailTab === 'overview' ? (
                <>
                  <div className={styles.detailGrid}>
                    <InfoItem label="下次执行" value={formatDateTime(selectedTask.nextRunAtMs)} />
                    <InfoItem label="最近执行" value={formatDateTime(selectedTask.lastRunAtMs)} />
                    <InfoItem label="并发/超时" value={`${selectedTask.execution.concurrency} / ${selectedTask.execution.timeoutMs}ms`} />
                    <InfoItem label="Dry-run" value={selectedTask.dryRun ? '开启' : '关闭'} />
                    <InfoItem label="范围" value={scopeLabel(selectedTask.targetScope)} />
                    <InfoItem label="日志保留" value={retentionLabel(selectedTask.logRetention)} />
                  </div>
                  <div className={styles.riskCard}>
                    <IconShield size={18} />
                    <div>
                      <strong>{selectedTask.dryRun ? 'dry-run 已开启' : '真实操作模式'}</strong>
                      <p>
                        未知状态、网络异常和巡检失败账号不会自动删除。自动删除需要明确允许并通过删除预览保护。
                      </p>
                    </div>
                  </div>
                </>
              ) : null}

              {detailTab === 'schedule' ? (
                <div className={styles.detailGrid}>
                  <InfoItem label="执行方式" value={scheduleTypeLabel(selectedTask.schedule)} />
                  <InfoItem label="计划" value={scheduleLabel(selectedTask.schedule)} />
                  <InfoItem
                    label="时区"
                    value={
                      selectedTask.schedule.type === 'interval' || selectedTask.schedule.type === 'daily_times'
                        ? selectedTask.schedule.timezone || '服务端默认'
                        : '服务端默认'
                    }
                  />
                  <InfoItem label="失败重试" value={`${selectedTask.execution.retries} 次`} />
                  <InfoItem label="并发数" value={String(selectedTask.execution.concurrency)} />
                  <InfoItem label="超时时间" value={`${selectedTask.execution.timeoutMs} ms`} />
                </div>
              ) : null}

              {detailTab === 'scope' ? (
                <div className={styles.scopePreview}>
                  <InfoItem label="范围类型" value={scopeLabel(selectedTask.targetScope)} />
                  {selectedTask.targetScope.type === 'files' ? (
                    <pre>{selectedTask.targetScope.fileNames.join('\n') || '--'}</pre>
                  ) : null}
                  {selectedTask.targetScope.type === 'auth_indices' ? (
                    <pre>{selectedTask.targetScope.authIndices.join('\n') || '--'}</pre>
                  ) : null}
                  {selectedTask.targetScope.type === 'metadata_filter' ? (
                    <div className={styles.detailGrid}>
                      <InfoItem label="关键词" value={selectedTask.targetScope.query || '--'} />
                      <InfoItem label="备注包含" value={selectedTask.targetScope.noteIncludes || '--'} />
                    </div>
                  ) : null}
                  {selectedTask.targetScope.type === 'all_codex' ? (
                    <p className={styles.mutedText}>将巡检 auth pool 中所有 Codex 账号。</p>
                  ) : null}
                </div>
              ) : null}

              {detailTab === 'policy' ? (
                <>
                  <div className={styles.strategyGrid}>
                    <PolicyBadge label="零额度" value={actionLabel(selectedTask.autoAction.zeroQuotaAction)} />
                    <PolicyBadge label="满额度" value={actionLabel(selectedTask.autoAction.fullQuotaAction)} />
                    <PolicyBadge
                      label="失效账号"
                      value={actionLabel(selectedTask.autoAction.invalidAction)}
                      danger={selectedTask.autoAction.invalidAction === 'delete'}
                    />
                    <PolicyBadge
                      label="自动删除"
                      value={selectedTask.autoAction.allowDelete ? '允许' : '关闭'}
                      danger={selectedTask.autoAction.allowDelete}
                    />
                    <PolicyBadge label="删除预览" value={selectedTask.autoAction.requireDeletePreview ? '必须' : '关闭'} />
                    <PolicyBadge label="Dry-run" value={selectedTask.autoAction.dryRun ? '开启' : '关闭'} />
                  </div>
                  {(selectedTask.autoAction.invalidAction === 'delete' || selectedTask.autoAction.allowDelete) ? (
                    <div className={styles.dangerNotice}>
                      <IconTrash2 size={18} />
                      <span>自动删除属于高风险操作，默认不会对 unknown、网络异常或巡检失败结果执行。</span>
                    </div>
                  ) : null}
                </>
              ) : null}

              {detailTab === 'notification' ? (
                <div className={styles.notificationPreview}>
                  <div className={styles.detailGrid}>
                    <InfoItem label="通知状态" value={selectedTask.notification.enabled ? '启用' : '停用'} />
                    <InfoItem label="触发条件" value={notificationTriggerLabel(selectedTask.notification.trigger)} />
                    <InfoItem label="渠道" value={selectedTask.notification.channels.join('、') || '--'} />
                    <InfoItem label="仅有操作/异常通知" value={selectedTask.notification.trigger === 'auto_action' || selectedTask.notification.trigger === 'abnormal' ? '是' : '否'} />
                  </div>
                  <div className={styles.channelPreviewGrid}>
                    {(['telegram', 'feishu', 'wecom', 'webhook'] as CodexInspectionNotificationChannel[]).map((channel) => (
                      <div key={channel} className={styles.channelPreviewCard}>
                        <strong>{channel}</strong>
                        <span>{selectedTask.notification.channels.includes(channel) ? '已选择' : '未选择'}</span>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {detailTab === 'logs' ? (
                <div className={styles.miniRunTable}>
                  {selectedTaskRuns.slice(0, 6).map((run) => (
                    <button key={run.id} type="button" onClick={() => openRunDetail(run)}>
                      <span className={`${styles.runStatus} ${statusTone(run.status)}`}>{statusLabel(run.status)}</span>
                      <strong>{formatDateTime(run.startedAtMs)}</strong>
                      <small>账号 {summaryNumber(run, 'total')} / 耗时 {formatDuration(run.durationMs)}</small>
                    </button>
                  ))}
                  {selectedTaskRuns.length === 0 ? <div className={styles.emptyRow}>暂无执行日志</div> : null}
                </div>
              ) : null}

              <div className={styles.detailFooter}>
                <ToggleSwitch
                  checked={selectedTask.enabled}
                  onChange={(checked) => void setTaskEnabled(selectedTask, checked)}
                  label={selectedTask.enabled ? '启用中' : '已停用'}
                />
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => runTask(selectedTask)}
                  loading={runningTaskIds.has(selectedTask.id)}
                >
                  <IconTimer size={15} />
                  手动运行
                </Button>
              </div>
            </>
          ) : (
            <div className={styles.emptyState}>
              <IconEye size={24} />
              <p>选择一个任务查看详情。</p>
            </div>
          )}
        </aside>
      </section>

      <Card className={styles.logsPanel}>
        <div className={styles.panelHeader}>
          <div>
            <h2>最近执行日志</h2>
            <p>点击一条执行记录查看账号结果、自动操作和通知发送记录。</p>
          </div>
        </div>
        <div className={styles.runTable}>
          <div className={styles.runTableHeader}>
            <span>任务</span>
            <span>状态</span>
            <span>触发</span>
            <span>账号</span>
            <span>操作</span>
            <span>开始时间</span>
            <span>耗时</span>
            <span />
          </div>
          {selectedTaskRuns.map((run) => (
            <button key={run.id} type="button" className={styles.runRow} onClick={() => openRunDetail(run)}>
              <span>{tasks.find((task) => task.id === run.taskId)?.name ?? run.taskId}</span>
              <span className={`${styles.runStatus} ${statusTone(run.status)}`}>{statusLabel(run.status)}</span>
              <span>{run.trigger}</span>
              <span>{summaryNumber(run, 'total')}</span>
              <span>
                禁用 {summaryNumber(run, 'disableCount')} / 启用 {summaryNumber(run, 'enableCount')} / 删除{' '}
                {summaryNumber(run, 'deleteCount')}
              </span>
              <span>{formatDateTime(run.startedAtMs)}</span>
              <span>{formatDuration(run.durationMs)}</span>
              <span className={styles.rowIcon}><IconEye size={16} /></span>
            </button>
          ))}
          {selectedTaskRuns.length === 0 ? <div className={styles.emptyRow}>暂无执行日志</div> : null}
        </div>
      </Card>

      <TaskModal
        open={taskModalOpen}
        mode={modalMode}
        draft={draft}
        wizardStep={wizardStep}
        saving={saving}
        onDraftChange={updateDraft}
        onToggleChannel={toggleNotificationChannel}
        onStepChange={setWizardStep}
        onClose={() => setTaskModalOpen(false)}
        onSave={saveTask}
      />

      <NotificationChannelModal
        open={notificationModalOpen}
        serviceBase={serviceBase}
        managementKey={managementKey}
        onClose={() => setNotificationModalOpen(false)}
        onNotify={showNotification}
      />

      <RunDetailModal
        open={runDetailOpen}
        detail={selectedRunDetail}
        actions={selectedRunActions}
        notifications={selectedRunNotifications}
        onClose={() => setRunDetailOpen(false)}
      />
    </div>
  );
}

function MetricCard({ label, value, meta, tone }: { label: string; value: string; meta: string; tone?: 'good' | 'info' | 'warn' }) {
  return (
    <Card className={`${styles.metricCard} ${tone ? styles[`metric-${tone}`] : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{meta}</small>
    </Card>
  );
}

function InfoItem({ label, value }: { label: string; value: string }) {
  return (
    <div className={styles.infoItem}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function PolicyBadge({ label, value, danger }: { label: string; value: string; danger?: boolean }) {
  return (
    <div className={`${styles.policyBadge} ${danger ? styles.policyDanger : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function retentionLabel(config: CodexInspectionLogRetentionConfig) {
  if (config.mode === 'days') return `${config.days} 天`;
  if (config.mode === 'latest') return `最近 ${config.count} 条`;
  return '不自动清理';
}

function TaskModal({
  open,
  mode,
  draft,
  wizardStep,
  saving,
  onDraftChange,
  onToggleChannel,
  onStepChange,
  onClose,
  onSave,
}: {
  open: boolean;
  mode: ModalMode;
  draft: TaskDraft;
  wizardStep: number;
  saving: boolean;
  onDraftChange: <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => void;
  onToggleChannel: (channel: CodexInspectionNotificationChannel) => void;
  onStepChange: (step: number) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const footer = (
    <div className={styles.modalFooter}>
      <Button variant="secondary" onClick={onClose} disabled={saving}>取消</Button>
      {wizardStep > 0 ? (
        <Button variant="secondary" onClick={() => onStepChange(wizardStep - 1)} disabled={saving}>上一步</Button>
      ) : null}
      {wizardStep < 4 ? (
        <Button onClick={() => onStepChange(wizardStep + 1)} disabled={saving}>下一步</Button>
      ) : (
        <Button onClick={onSave} loading={saving}>{mode === 'edit' ? '保存任务' : '创建任务'}</Button>
      )}
    </div>
  );

  return (
    <Modal open={open} title={mode === 'edit' ? '编辑巡检任务' : '新建巡检任务'} onClose={onClose} footer={footer} width={980}>
      <div className={styles.wizardSteps}>
        {['基本信息', '检测配置', '巡检范围', '执行策略', '通知与日志'].map((label, index) => (
          <button
            key={label}
            type="button"
            className={index === wizardStep ? styles.stepActive : ''}
            onClick={() => onStepChange(index)}
          >
            <span>{index + 1}</span>
            {label}
          </button>
        ))}
      </div>

      {wizardStep === 0 ? (
        <div className={styles.formGrid}>
          <Input label="任务名称" value={draft.name} onChange={(event) => onDraftChange('name', event.target.value)} />
          <Input label="描述" value={draft.description} onChange={(event) => onDraftChange('description', event.target.value)} />
          <ToggleSwitch checked={draft.enabled} onChange={(value) => onDraftChange('enabled', value)} label="启用任务" />
          <ToggleSwitch checked={draft.dryRun} onChange={(value) => onDraftChange('dryRun', value)} label="Dry-run 模式" />
          <div className={styles.safeNotice}>
            <IconShield size={18} />
            <span>默认开启 dry-run，只生成建议和审计记录，不会实际修改 Codex 账号。</span>
          </div>
        </div>
      ) : null}

      {wizardStep === 1 ? (
        <div className={styles.formGrid}>
          <Input label="并发数" type="number" min={1} value={draft.concurrency} onChange={(event) => onDraftChange('concurrency', event.target.value)} />
          <Input label="超时时间 ms" type="number" min={1000} value={draft.timeoutMs} onChange={(event) => onDraftChange('timeoutMs', event.target.value)} />
          <Input label="失败重试次数" type="number" min={0} value={draft.retries} onChange={(event) => onDraftChange('retries', event.target.value)} />
          <ToggleSwitch checked={draft.saveLogs} onChange={(value) => onDraftChange('saveLogs', value)} label="保存任务日志" />
          <div className={styles.safeNotice}>
            <IconFileText size={18} />
            <span>关闭任务日志不会关闭自动处理审计，系统仍会保存最小审计信息。</span>
          </div>
        </div>
      ) : null}

      {wizardStep === 2 ? (
        <div className={styles.formGrid}>
          <label className={styles.field}>
            <span>巡检范围</span>
            <select value={draft.targetType} onChange={(event) => onDraftChange('targetType', event.target.value as TaskDraft['targetType'])}>
              <option value="all_codex">全部 Codex 账号</option>
              <option value="files">指定认证文件</option>
              <option value="auth_indices">指定 auth_index</option>
              <option value="metadata_filter">元数据筛选</option>
            </select>
          </label>
          {draft.targetType === 'files' ? (
            <label className={styles.fieldWide}>
              <span>认证文件名</span>
              <textarea value={draft.fileNames} onChange={(event) => onDraftChange('fileNames', event.target.value)} placeholder="每行一个文件名，或用逗号分隔" />
            </label>
          ) : null}
          {draft.targetType === 'auth_indices' ? (
            <label className={styles.fieldWide}>
              <span>auth_index</span>
              <textarea value={draft.authIndices} onChange={(event) => onDraftChange('authIndices', event.target.value)} placeholder="每行一个 auth_index，或用逗号分隔" />
            </label>
          ) : null}
          {draft.targetType === 'metadata_filter' ? (
            <>
              <Input label="关键词" value={draft.query} onChange={(event) => onDraftChange('query', event.target.value)} />
              <Input label="备注包含" value={draft.noteIncludes} onChange={(event) => onDraftChange('noteIncludes', event.target.value)} />
            </>
          ) : null}
          <div className={styles.safeNotice}>
            <IconSearch size={18} />
            <span>当前版本没有独立标签系统，标签筛选会复用 auth file 中可搜索的备注、账号和 provider 信息。</span>
          </div>
        </div>
      ) : null}

      {wizardStep === 3 ? (
        <div className={styles.formGrid}>
          <label className={styles.field}>
            <span>执行方式</span>
            <select value={draft.scheduleType} onChange={(event) => onDraftChange('scheduleType', event.target.value as TaskDraft['scheduleType'])}>
              <option value="manual">手动执行</option>
              <option value="interval">固定频率</option>
              <option value="daily_times">多个指定时间点</option>
            </select>
          </label>
          {draft.scheduleType === 'interval' ? (
            <>
              <Input label="每 N" type="number" min={1} value={draft.intervalEvery} onChange={(event) => onDraftChange('intervalEvery', event.target.value)} />
              <label className={styles.field}>
                <span>单位</span>
                <select value={draft.intervalUnit} onChange={(event) => onDraftChange('intervalUnit', event.target.value as TaskDraft['intervalUnit'])}>
                  <option value="minute">分钟</option>
                  <option value="hour">小时</option>
                  <option value="day">天</option>
                </select>
              </label>
            </>
          ) : null}
          {draft.scheduleType === 'daily_times' ? (
            <Input label="执行时间点" value={draft.dailyTimes} onChange={(event) => onDraftChange('dailyTimes', event.target.value)} hint="示例：09:00,13:00,23:30" />
          ) : null}
          <Input label="时区" value={draft.timezone} onChange={(event) => onDraftChange('timezone', event.target.value)} placeholder="Asia/Shanghai" />
          <label className={styles.field}>
            <span>日志保留</span>
            <select value={draft.retentionMode} onChange={(event) => onDraftChange('retentionMode', event.target.value as TaskDraft['retentionMode'])}>
              <option value="days">按天数</option>
              <option value="latest">保留最近 N 条</option>
              <option value="none">不自动清理</option>
            </select>
          </label>
          {draft.retentionMode === 'days' ? (
            <Input label="保留天数" type="number" min={1} value={draft.retentionDays} onChange={(event) => onDraftChange('retentionDays', event.target.value)} />
          ) : null}
          {draft.retentionMode === 'latest' ? (
            <Input label="保留最近条数" type="number" min={1} value={draft.retentionCount} onChange={(event) => onDraftChange('retentionCount', event.target.value)} />
          ) : null}
          <label className={styles.field}>
            <span>零额度账号</span>
            <select value={draft.zeroQuotaAction} onChange={(event) => onDraftChange('zeroQuotaAction', event.target.value as TaskDraft['zeroQuotaAction'])}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>满额度账号</span>
            <select value={draft.fullQuotaAction} onChange={(event) => onDraftChange('fullQuotaAction', event.target.value as TaskDraft['fullQuotaAction'])}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>失效账号</span>
            <select value={draft.invalidAction} onChange={(event) => onDraftChange('invalidAction', event.target.value as CodexInspectionAutoAction)}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
              <option value="delete">自动删除</option>
            </select>
          </label>
          <ToggleSwitch checked={draft.allowDelete} onChange={(value) => onDraftChange('allowDelete', value)} label="允许自动删除" />
          <ToggleSwitch checked={draft.requireDeletePreview} onChange={(value) => onDraftChange('requireDeletePreview', value)} label="删除前必须预览" />
          {(draft.invalidAction === 'delete' || draft.allowDelete) ? (
            <div className={styles.dangerNotice}>
              <IconTrash2 size={18} />
              <span>自动删除默认关闭。实际删除需要关闭 dry-run、开启允许自动删除，并关闭预览保护。</span>
            </div>
          ) : null}
          <div className={styles.safeNotice}>
            <IconShield size={18} />
            <span>未知状态、网络异常和巡检失败账号不会执行自动处理。</span>
          </div>
        </div>
      ) : null}

      {wizardStep === 4 ? (
        <div className={styles.formGrid}>
          <ToggleSwitch checked={draft.notificationEnabled} onChange={(value) => onDraftChange('notificationEnabled', value)} label="启用通知" />
          <label className={styles.field}>
            <span>通知触发条件</span>
            <select value={draft.notificationTrigger} onChange={(event) => onDraftChange('notificationTrigger', event.target.value as CodexInspectionNotificationTrigger)}>
              <option value="always">每次巡检</option>
              <option value="abnormal">仅异常</option>
              <option value="auto_action">仅有自动操作</option>
              <option value="manual_required">仅需人工处理</option>
            </select>
          </label>
          <div className={styles.channelGroup}>
            {(['telegram', 'feishu', 'wecom', 'webhook'] as CodexInspectionNotificationChannel[]).map((channel) => (
              <label key={channel} className={styles.checkboxPill}>
                <input
                  type="checkbox"
                  checked={draft.notificationChannels.includes(channel)}
                  onChange={() => onToggleChannel(channel)}
                />
                <span>{channel}</span>
              </label>
            ))}
          </div>
          {draft.notificationChannels.includes('telegram') ? (
            <>
              <Input label="Telegram Bot Token" value={draft.telegramBotToken} onChange={(event) => onDraftChange('telegramBotToken', event.target.value)} />
              <Input label="Telegram Chat ID" value={draft.telegramChatId} onChange={(event) => onDraftChange('telegramChatId', event.target.value)} />
            </>
          ) : null}
          {draft.notificationChannels.includes('feishu') ? (
            <>
              <Input label="飞书机器人 Webhook" value={draft.feishuWebhookUrl} onChange={(event) => onDraftChange('feishuWebhookUrl', event.target.value)} />
              <Input label="飞书 Secret" value={draft.feishuSecret} onChange={(event) => onDraftChange('feishuSecret', event.target.value)} />
            </>
          ) : null}
          {draft.notificationChannels.includes('wecom') ? (
            <Input label="企业微信机器人 Webhook" value={draft.wecomWebhookUrl} onChange={(event) => onDraftChange('wecomWebhookUrl', event.target.value)} />
          ) : null}
          <Input label="自定义 Webhook URL" value={draft.webhookUrl} onChange={(event) => onDraftChange('webhookUrl', event.target.value)} />
          <label className={styles.fieldWide}>
            <span>Webhook Header</span>
            <textarea value={draft.webhookHeaders} onChange={(event) => onDraftChange('webhookHeaders', event.target.value)} placeholder="Authorization: Bearer xxx" />
          </label>
        </div>
      ) : null}
    </Modal>
  );
}

function NotificationChannelModal({
  open,
  serviceBase,
  managementKey,
  onClose,
  onNotify,
}: {
  open: boolean;
  serviceBase: string;
  managementKey?: string;
  onClose: () => void;
  onNotify: (message: string, type?: 'success' | 'error' | 'warning' | 'info') => void;
}) {
  const [channel, setChannel] = useState<CodexInspectionNotificationChannel>('telegram');
  const [enabled, setEnabled] = useState(true);
  const [botToken, setBotToken] = useState('');
  const [chatId, setChatId] = useState('');
  const [webhookUrl, setWebhookUrl] = useState('');
  const [secret, setSecret] = useState('');
  const [headers, setHeaders] = useState('Content-Type: application/json');
  const [template, setTemplate] = useState(
    'Codex 巡检任务：{{taskName}}\n状态：{{status}}\n账号总数：{{total}}\n日志 ID：{{logId}}'
  );
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState('');

  const channelConfig = useMemo(() => {
    if (channel === 'telegram') {
      return { botToken, chatId, template };
    }
    if (channel === 'feishu') {
      return { webhookUrl, secret, template };
    }
    if (channel === 'wecom') {
      return { webhookUrl, template };
    }
    return { url: webhookUrl, headers: parseHeaders(headers), template };
  }, [botToken, channel, chatId, headers, secret, template, webhookUrl]);

  const payload = useMemo(
    () => ({
      enabled,
      channels: enabled ? [channel] : [],
      trigger: 'always',
      channelConfigs: {
        [channel]: channelConfig,
      },
    }),
    [channel, channelConfig, enabled]
  );

  const previewText = `Codex 巡检任务：Codex 巡检通知测试
状态：success
账号总数：1
正常：1，零额度：0，满额度：0，失效：0，失败：0
日志 ID：test`;

  const testNotification = async () => {
    if (!serviceBase) {
      onNotify('Usage Service 未连接，无法测试通知', 'error');
      return;
    }
    setTesting(true);
    setTestResult('');
    try {
      const response = await usageServiceApi.testCodexInspectionNotification(
        serviceBase,
        { notification: payload },
        managementKey
      );
      setTestResult(JSON.stringify(response, null, 2));
      onNotify(Boolean(response.ok) ? '测试通知发送成功' : '测试通知发送失败', Boolean(response.ok) ? 'success' : 'warning');
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setTestResult(message);
      onNotify(message, 'error');
    } finally {
      setTesting(false);
    }
  };

  return (
    <Modal open={open} title="通知渠道配置" onClose={onClose} width={920}>
      <div className={styles.notificationModalGrid}>
        <section className={styles.notificationForm}>
          <ToggleSwitch checked={enabled} onChange={setEnabled} label="启用通知渠道" />
          <label className={styles.field}>
            <span>渠道</span>
            <select value={channel} onChange={(event) => setChannel(event.target.value as CodexInspectionNotificationChannel)}>
              <option value="telegram">Telegram Bot</option>
              <option value="feishu">飞书机器人</option>
              <option value="wecom">企业微信机器人</option>
              <option value="webhook">自定义 Webhook</option>
            </select>
          </label>

          {channel === 'telegram' ? (
            <>
              <Input
                label="Bot Token"
                value={botToken}
                onChange={(event) => setBotToken(event.target.value)}
                placeholder="保存后后端应脱敏返回"
              />
              <Input label="Chat ID" value={chatId} onChange={(event) => setChatId(event.target.value)} />
            </>
          ) : null}

          {channel === 'feishu' || channel === 'wecom' || channel === 'webhook' ? (
            <Input
              label={channel === 'webhook' ? 'Webhook URL' : '机器人 Webhook'}
              value={webhookUrl}
              onChange={(event) => setWebhookUrl(event.target.value)}
              placeholder="保存后不应明文回显"
            />
          ) : null}

          {channel === 'feishu' ? (
            <Input label="Secret" value={secret} onChange={(event) => setSecret(event.target.value)} />
          ) : null}

          {channel === 'webhook' ? (
            <label className={styles.fieldWide}>
              <span>Header</span>
              <textarea value={headers} onChange={(event) => setHeaders(event.target.value)} />
            </label>
          ) : null}

          <label className={styles.fieldWide}>
            <span>消息模板</span>
            <textarea value={template} onChange={(event) => setTemplate(event.target.value)} />
          </label>

          <div className={styles.safeNotice}>
            <IconShield size={18} />
            <span>Token、Secret、Webhook URL 保存后需要由后端脱敏显示；测试通知失败不会阻止任务配置保存。</span>
          </div>

          <div className={styles.modalFooter}>
            <Button variant="secondary" onClick={onClose}>关闭</Button>
            <Button onClick={() => void testNotification()} loading={testing}>测试通知</Button>
          </div>
        </section>

        <aside className={styles.notificationPreviewPanel}>
          <div>
            <h3>消息预览</h3>
            <pre>{previewText}</pre>
          </div>
          <div>
            <h3>JSON Payload</h3>
            <pre>{JSON.stringify(payload, null, 2)}</pre>
          </div>
          {testResult ? (
            <div>
              <h3>测试结果</h3>
              <pre>{testResult}</pre>
            </div>
          ) : null}
        </aside>
      </div>
    </Modal>
  );
}

function RunDetailModal({
  open,
  detail,
  actions,
  notifications,
  onClose,
}: {
  open: boolean;
  detail: CodexInspectionRunResponse | null;
  actions: CodexInspectionActionRecord[];
  notifications: CodexInspectionNotificationRecord[];
  onClose: () => void;
}) {
  const run = detail?.run;
  return (
    <Modal open={open} title="执行日志详情" onClose={onClose} width={900}>
      {run ? (
        <div className={styles.runDetail}>
          <div className={styles.detailGrid}>
            <InfoItem label="日志 ID" value={run.id} />
            <InfoItem label="批次 ID" value={run.batchId} />
            <InfoItem label="状态" value={statusLabel(run.status)} />
            <InfoItem label="触发方式" value={run.trigger} />
            <InfoItem label="开始时间" value={formatDateTime(run.startedAtMs)} />
            <InfoItem label="耗时" value={formatDuration(run.durationMs)} />
          </div>
          <div className={styles.resultSummary}>
            <PolicyBadge label="账号总数" value={String(summaryNumber(run, 'total'))} />
            <PolicyBadge label="正常" value={String(summaryNumber(run, 'healthy'))} />
            <PolicyBadge label="零额度" value={String(summaryNumber(run, 'zeroQuota'))} />
            <PolicyBadge label="满额度" value={String(summaryNumber(run, 'fullQuota'))} />
            <PolicyBadge label="失效" value={String(summaryNumber(run, 'invalid'))} />
            <PolicyBadge label="探测失败" value={String(summaryNumber(run, 'probeFailed'))} />
          </div>
          <section>
            <h3>账号结果</h3>
            <div className={styles.accountRows}>
              {(detail.accounts ?? []).map((account) => (
                <div key={account.id ?? `${account.runId}-${account.fileName}`} className={styles.accountRow}>
                  <strong>{account.displayAccount || account.fileName}</strong>
                  <span>{account.fileName}</span>
                  <span>{account.classification}</span>
                  <span>{account.recommendedAction}</span>
                  <small>{account.actionReason}</small>
                </div>
              ))}
            </div>
          </section>
          <section>
            <h3>自动操作</h3>
            <div className={styles.auditRows}>
              {actions.map((action) => (
                <div key={action.id ?? `${action.runId}-${action.fileName}-${action.action}`} className={styles.auditRow}>
                  <span>{action.action}</span>
                  <strong>{action.fileName}</strong>
                  <span>{action.dryRun ? 'dry-run' : 'real'}</span>
                  <span className={action.success ? styles.toneGood : styles.toneBad}>
                    {action.success ? '成功' : '失败'}
                  </span>
                  <small>{action.error || action.triggerReason}</small>
                </div>
              ))}
              {actions.length === 0 ? <div className={styles.emptyRow}>无自动操作</div> : null}
            </div>
          </section>
          <section>
            <h3>通知结果</h3>
            <div className={styles.auditRows}>
              {notifications.map((record) => (
                <div key={record.id ?? `${record.runId}-${record.channel}`} className={styles.auditRow}>
                  <span>{record.channel}</span>
                  <strong className={record.status === 'success' ? styles.toneGood : styles.toneBad}>
                    {record.status}
                  </strong>
                  <small>{record.error || record.responseSummary}</small>
                </div>
              ))}
              {notifications.length === 0 ? <div className={styles.emptyRow}>无通知记录</div> : null}
            </div>
          </section>
        </div>
      ) : null}
    </Modal>
  );
}
