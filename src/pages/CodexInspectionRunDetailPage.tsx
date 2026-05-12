import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import {
  IconChartLine,
  IconExternalLink,
  IconEye,
  IconFileText,
  IconRefreshCw,
  IconShield,
  IconTimer,
} from '@/components/ui/icons';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore, useUsageServiceStore } from '@/stores';
import type {
  CodexInspectionAccountResult,
  CodexInspectionRun,
  CodexInspectionRunResponse,
} from '@/types/codexInspectionTask';
import { detectApiBaseFromLocation } from '@/utils/connection';
import styles from './CodexInspectionRunDetailPage.module.scss';

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
    case 'missed':
      return '已错过';
    default:
      return status || '未知';
  }
};

const statusTone = (status?: string) => {
  if (status === 'success' || status === 'healthy' || status === 'normal') return styles.toneGood;
  if (status === 'partial' || status === 'zero_quota' || status === 'full_quota') return styles.toneWarn;
  if (status === 'failed' || status === 'invalid' || status === 'probe_failed') return styles.toneBad;
  if (status === 'running' || status === 'queued') return styles.toneInfo;
  return styles.toneMuted;
};

const classificationLabel = (value?: string) => {
  switch (value) {
    case 'healthy':
    case 'normal':
      return '正常';
    case 'zero_quota':
      return '零额度';
    case 'full_quota':
      return '满额度';
    case 'invalid':
      return '失效';
    case 'probe_failed':
    case 'failed':
      return '巡检失败';
    case 'unknown':
      return '未知';
    default:
      return value || '--';
  }
};

const accountSearchText = (account: CodexInspectionAccountResult) =>
  [
    account.displayAccount,
    account.accountId,
    account.authIndex,
    account.fileName,
    account.provider,
    account.error,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();

export function CodexInspectionRunDetailPage() {
  const { runId = '' } = useParams();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [serviceBase, setServiceBase] = useState('');
  const [detail, setDetail] = useState<CodexInspectionRunResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [statusFilter, setStatusFilter] = useState('all');
  const [actionFilter, setActionFilter] = useState('all');
  const [keyword, setKeyword] = useState('');

  const resolveServiceBase = useCallback(async () => {
    if (usageServiceEnabled && usageServiceBase) return usageServiceBase;
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
        // 主服务未代理 Usage Service 时继续尝试下一个候选地址。
      }
    }
    return '';
  }, [apiBase, usageServiceBase, usageServiceEnabled]);

  const loadDetail = useCallback(async () => {
    setLoading(true);
    try {
      const base = await resolveServiceBase();
      setServiceBase(base);
      if (!base || !runId) {
        setDetail(null);
        return;
      }
      const response = await usageServiceApi.getCodexInspectionRun(base, runId, managementKey);
      setDetail(response);
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setLoading(false);
    }
  }, [managementKey, resolveServiceBase, runId, showNotification]);

  useEffect(() => {
    void loadDetail();
  }, [loadDetail]);

  const run = detail?.run ?? null;
  const accounts = detail?.accounts ?? [];
  const actions = detail?.actions ?? [];
  const notifications = detail?.notifications ?? [];

  const filteredAccounts = useMemo(() => {
    const normalizedKeyword = keyword.trim().toLowerCase();
    return accounts.filter((account) => {
      const classification = account.classification || account.status || 'unknown';
      const action = account.recommendedAction || 'none';
      if (statusFilter !== 'all' && classification !== statusFilter && account.status !== statusFilter) {
        return false;
      }
      if (actionFilter !== 'all' && action !== actionFilter) return false;
      if (normalizedKeyword && !accountSearchText(account).includes(normalizedKeyword)) return false;
      return true;
    });
  }, [accounts, actionFilter, keyword, statusFilter]);

  const actionStats = useMemo(
    () => ({
      disabled: actions.filter((item) => item.action === 'disable' && item.success).length,
      enabled: actions.filter((item) => item.action === 'enable' && item.success).length,
      deleted: actions.filter((item) => item.action === 'delete' && item.success).length,
      failed: actions.filter((item) => !item.success).length,
    }),
    [actions]
  );

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <p className={styles.eyebrow}>Codex Account Inspection</p>
          <h1>执行日志详情</h1>
          <p>查看单次巡检的账号明细、通知发送结果和自动操作审计。</p>
        </div>
        <div className={styles.headerActions}>
          <Link to="/monitoring/codex-inspection-tasks" className={styles.secondaryLink}>
            <IconTimer size={16} />
            <span>巡检任务</span>
            <IconExternalLink size={14} />
          </Link>
          <Link to="/monitoring/codex-inspection" className={styles.secondaryLink}>
            <IconChartLine size={16} />
            <span>手动巡检</span>
          </Link>
          <Button variant="secondary" onClick={loadDetail} loading={loading}>
            <IconRefreshCw size={16} />
            刷新
          </Button>
        </div>
      </header>

      {!serviceBase && !loading ? (
        <Card className={styles.notice}>
          <IconFileText size={20} />
          <div>
            <strong>Usage Service 未连接</strong>
            <p>执行日志详情需要从 Usage Service 读取，请先确认服务已启用。</p>
          </div>
        </Card>
      ) : null}

      {run ? (
        <>
          <Card className={styles.runHeader}>
            <div>
              <span className={`${styles.statusPill} ${statusTone(run.status)}`}>
                {statusLabel(run.status)}
              </span>
              <h2>{run.id}</h2>
              <p>批次 ID：{run.batchId || '--'}</p>
            </div>
            <div className={styles.metaGrid}>
              <InfoItem label="任务 ID" value={run.taskId} />
              <InfoItem label="触发方式" value={run.trigger} />
              <InfoItem label="开始时间" value={formatDateTime(run.startedAtMs)} />
              <InfoItem label="结束时间" value={formatDateTime(run.endedAtMs)} />
              <InfoItem label="耗时" value={formatDuration(run.durationMs)} />
              <InfoItem label="执行模式" value={run.autoActionSnapshot?.dryRun ? 'dry-run' : 'real'} />
            </div>
          </Card>

          <section className={styles.summaryGrid}>
            <MetricCard label="账号总数" value={summaryNumber(run, 'total')} />
            <MetricCard label="正常" value={summaryNumber(run, 'healthy')} tone="good" />
            <MetricCard label="零额度" value={summaryNumber(run, 'zeroQuota')} tone="warn" />
            <MetricCard label="满额度" value={summaryNumber(run, 'fullQuota')} tone="warn" />
            <MetricCard label="失效" value={summaryNumber(run, 'invalid')} tone="bad" />
            <MetricCard label="巡检失败" value={summaryNumber(run, 'probeFailed')} tone="bad" />
          </section>

          <section className={styles.contentGrid}>
            <Card className={styles.accountPanel}>
              <div className={styles.panelHeader}>
                <div>
                  <h2>账号明细</h2>
                  <p>支持按判定状态、建议动作和关键词筛选。</p>
                </div>
              </div>
              <div className={styles.filters}>
                <label className={styles.field}>
                  <span>状态</span>
                  <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
                    <option value="all">全部</option>
                    <option value="healthy">正常</option>
                    <option value="zero_quota">零额度</option>
                    <option value="full_quota">满额度</option>
                    <option value="invalid">失效</option>
                    <option value="probe_failed">巡检失败</option>
                    <option value="unknown">未知</option>
                  </select>
                </label>
                <label className={styles.field}>
                  <span>动作</span>
                  <select value={actionFilter} onChange={(event) => setActionFilter(event.target.value)}>
                    <option value="all">全部</option>
                    <option value="none">无</option>
                    <option value="disable">禁用</option>
                    <option value="enable">启用</option>
                    <option value="delete">删除</option>
                  </select>
                </label>
                <Input
                  label="关键词"
                  value={keyword}
                  onChange={(event) => setKeyword(event.target.value)}
                  placeholder="邮箱 / 账号名 / auth file / provider"
                />
              </div>
              <div className={styles.accountTable}>
                <div className={styles.accountTableHeader}>
                  <span>账号</span>
                  <span>HTTP</span>
                  <span>配额</span>
                  <span>判定结果</span>
                  <span>建议动作</span>
                  <span>错误摘要</span>
                </div>
                {filteredAccounts.map((account) => (
                  <div key={account.id ?? `${account.runId}-${account.fileName}-${account.authIndex ?? ''}`} className={styles.accountRow}>
                    <span>
                      <strong>{account.displayAccount || account.accountId || account.fileName}</strong>
                      <small>{account.fileName}{account.authIndex ? ` / ${account.authIndex}` : ''}</small>
                    </span>
                    <span>{account.statusCode ?? '--'}</span>
                    <span>{typeof account.usedPercent === 'number' ? `${account.usedPercent}%` : '--'}</span>
                    <span className={`${styles.statusPill} ${statusTone(account.classification || account.status)}`}>
                      {classificationLabel(account.classification || account.status)}
                    </span>
                    <span>{account.recommendedAction || 'none'}</span>
                    <span>{account.error || account.actionReason || '--'}</span>
                  </div>
                ))}
                {filteredAccounts.length === 0 ? <div className={styles.emptyRow}>没有匹配的账号结果</div> : null}
              </div>
            </Card>

            <aside className={styles.sideColumn}>
              <Card className={styles.sideCard}>
                <h3>结果分布</h3>
                <DistributionBar run={run} />
              </Card>
              <Card className={styles.sideCard}>
                <h3>通知发送结果</h3>
                <div className={styles.auditRows}>
                  {notifications.map((record) => (
                    <div key={record.id ?? `${record.runId}-${record.channel}`} className={styles.auditRow}>
                      <span>{record.channel}</span>
                      <strong className={record.status === 'success' ? styles.toneGood : styles.toneBad}>
                        {record.status}
                      </strong>
                      <small>{record.error || record.responseSummary || '--'}</small>
                    </div>
                  ))}
                  {notifications.length === 0 ? <div className={styles.emptyRow}>无通知记录</div> : null}
                </div>
              </Card>
              <Card className={styles.sideCard}>
                <h3>自动操作审计</h3>
                <div className={styles.actionStats}>
                  <InfoItem label="禁用成功" value={String(actionStats.disabled)} />
                  <InfoItem label="启用成功" value={String(actionStats.enabled)} />
                  <InfoItem label="删除成功" value={String(actionStats.deleted)} />
                  <InfoItem label="操作失败" value={String(actionStats.failed)} />
                </div>
                <div className={styles.auditRows}>
                  {actions.slice(0, 8).map((action) => (
                    <div key={action.id ?? `${action.runId}-${action.fileName}-${action.action}`} className={styles.auditRow}>
                      <span>{action.action}</span>
                      <strong>{action.fileName}</strong>
                      <small className={action.success ? styles.toneGood : styles.toneBad}>
                        {action.dryRun ? 'dry-run' : 'real'} / {action.success ? '成功' : '失败'}
                      </small>
                    </div>
                  ))}
                  {actions.length === 0 ? <div className={styles.emptyRow}>无自动操作</div> : null}
                </div>
              </Card>
              <Card className={styles.sideCard}>
                <h3>安全说明</h3>
                <div className={styles.safetyNote}>
                  <IconShield size={18} />
                  <p>未知状态、网络异常和巡检失败账号不会进入自动删除分支，删除动作仍受 dry-run 和 allow delete 保护。</p>
                </div>
              </Card>
            </aside>
          </section>
        </>
      ) : loading ? (
        <Card className={styles.emptyState}>
          <span className={styles.spinner} />
          <p>正在加载执行日志...</p>
        </Card>
      ) : (
        <Card className={styles.emptyState}>
          <IconEye size={24} />
          <p>未找到执行日志。</p>
        </Card>
      )}
    </div>
  );
}

function MetricCard({ label, value, tone }: { label: string; value: number; tone?: 'good' | 'warn' | 'bad' }) {
  return (
    <Card className={`${styles.metricCard} ${tone ? styles[`metric-${tone}`] : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
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

function DistributionBar({ run }: { run: CodexInspectionRun }) {
  const total = Math.max(summaryNumber(run, 'total'), 1);
  const segments = [
    { key: 'healthy', label: '正常', value: summaryNumber(run, 'healthy'), className: styles.segmentGood },
    { key: 'zeroQuota', label: '零额度', value: summaryNumber(run, 'zeroQuota'), className: styles.segmentWarn },
    { key: 'fullQuota', label: '满额度', value: summaryNumber(run, 'fullQuota'), className: styles.segmentInfo },
    { key: 'invalid', label: '失效', value: summaryNumber(run, 'invalid'), className: styles.segmentBad },
    { key: 'probeFailed', label: '失败', value: summaryNumber(run, 'probeFailed'), className: styles.segmentMuted },
  ];

  return (
    <div className={styles.distribution}>
      <div className={styles.distributionBar}>
        {segments.map((segment) => (
          <span
            key={segment.key}
            className={segment.className}
            style={{ width: `${Math.max((segment.value / total) * 100, segment.value > 0 ? 4 : 0)}%` }}
          />
        ))}
      </div>
      <div className={styles.distributionLegend}>
        {segments.map((segment) => (
          <span key={segment.key}>
            {segment.label} {segment.value}
          </span>
        ))}
      </div>
    </div>
  );
}
