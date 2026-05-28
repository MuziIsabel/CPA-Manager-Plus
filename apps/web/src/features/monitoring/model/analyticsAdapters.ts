import type {
  MonitoringAnalyticsChannelShareRow,
  MonitoringAnalyticsEventRow,
  MonitoringAnalyticsFailureSourceRow,
  MonitoringAnalyticsFilters,
  MonitoringAnalyticsHourlyPoint,
  MonitoringAnalyticsModelShareRow,
  MonitoringAnalyticsModelStat,
  MonitoringAnalyticsRecentFailure,
  MonitoringAnalyticsSummary,
  MonitoringAnalyticsTaskBucketRow,
  MonitoringAnalyticsTimelinePoint,
} from '@/services/api/usageService';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import { normalizeAuthIndex, type UsageDetailWithEndpoint } from '@/utils/usage';
import { joinUnique, maskAuthIndex, maskEmailLike, readString } from './base';
import { buildDayLabel, buildHourLabel, buildLocalDayKey, padNumber } from './range';
import type {
  MonitoringAuthMeta,
  MonitoringChannelMeta,
  MonitoringChannelRow,
  MonitoringFailureRow,
  MonitoringFailureSourceRow,
  MonitoringModelRow,
  MonitoringModelShareRow,
  MonitoringScopeFilters,
  MonitoringSummary,
  MonitoringTaskBucketRow,
  MonitoringTimelinePoint,
} from './types';

const isActiveFilterValue = (value: string | null | undefined) =>
  Boolean(value && value.trim() && value !== 'all');

const shortHashLabel = (value: string) => {
  const trimmed = value.trim();
  if (!trimmed) return '-';
  return trimmed.length <= 12 ? trimmed : `${trimmed.slice(0, 6)}...${trimmed.slice(-4)}`;
};

const addAuthIndexConstraint = (
  current: Set<string> | null,
  values: Iterable<string>
): Set<string> | null => {
  const next = new Set(Array.from(values).map(normalizeAuthIndex).filter(Boolean) as string[]);
  if (next.size === 0) return current;
  if (current === null) return next;
  return new Set(Array.from(current).filter((value) => next.has(value)));
};

export const buildAnalyticsFilters = (
  scopeFilters: MonitoringScopeFilters | undefined,
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channels: MonitoringChannelMeta[]
): MonitoringAnalyticsFilters => {
  const filters: MonitoringAnalyticsFilters = {};
  if (!scopeFilters) return filters;

  if (isActiveFilterValue(scopeFilters.model)) {
    filters.models = [scopeFilters.model!.trim()];
  }
  if (isActiveFilterValue(scopeFilters.apiKeyHash)) {
    filters.api_key_hashes = [scopeFilters.apiKeyHash!.trim().toLowerCase()];
  }
  if (scopeFilters.status === 'success') {
    filters.include_failed = false;
  } else if (scopeFilters.status === 'failed') {
    filters.failed_only = true;
  }

  let authIndices: Set<string> | null = null;
  if (isActiveFilterValue(scopeFilters.account)) {
    const account = scopeFilters.account!.trim();
    authIndices = addAuthIndexConstraint(
      authIndices,
      Array.from(authMetaMap.entries())
        .filter(([, meta]) => meta.account === account)
        .map(([authIndex]) => authIndex)
    );
  }
  if (isActiveFilterValue(scopeFilters.provider)) {
    const provider = scopeFilters.provider!.trim();
    authIndices = addAuthIndexConstraint(
      authIndices,
      Array.from(authMetaMap.entries())
        .filter(([, meta]) => meta.provider === provider)
        .map(([authIndex]) => authIndex)
    );
  }
  if (isActiveFilterValue(scopeFilters.channel)) {
    const channel = scopeFilters.channel!.trim();
    authIndices = addAuthIndexConstraint(
      authIndices,
      channels.filter((item) => item.name === channel).flatMap((item) => item.authIndices)
    );
  }
  if (authIndices && authIndices.size > 0) {
    filters.auth_indices = Array.from(authIndices).sort();
  }

  return filters;
};

export const buildSummaryFromAnalytics = (
  summary: MonitoringAnalyticsSummary
): MonitoringSummary => ({
  totalCalls: summary.total_calls,
  successCalls: summary.success_calls,
  failureCalls: summary.failure_calls,
  successRate: summary.success_rate,
  inputTokens: summary.input_tokens,
  outputTokens: summary.output_tokens,
  reasoningTokens: summary.reasoning_tokens,
  cachedTokens: summary.cached_tokens,
  cacheReadTokens: summary.cache_read_tokens ?? 0,
  cacheCreationTokens: summary.cache_creation_tokens ?? 0,
  totalTokens: summary.total_tokens,
  totalCost: summary.total_cost,
  averageLatencyMs: summary.average_latency_ms,
  rpm30m: summary.rpm_30m,
  tpm30m: summary.tpm_30m,
  avgDailyRequests: summary.avg_daily_requests,
  avgDailyTokens: summary.avg_daily_tokens,
  approxTasks: summary.approx_tasks,
  approxTaskFailures: summary.approx_task_failures,
  approxTaskSuccessRate: summary.approx_task_success_rate,
  zeroTokenCalls: summary.zero_token_calls,
  zeroTokenModels: summary.zero_token_models,
});

export const buildTimelineFromAnalytics = (
  points: MonitoringAnalyticsTimelinePoint[],
  granularity: 'hour' | 'day' | string
): MonitoringTimelinePoint[] =>
  points.map((point) => ({
    label:
      granularity === 'hour'
        ? buildHourLabel(point.bucket_ms)
        : buildDayLabel(buildLocalDayKey(point.bucket_ms)),
    requests: point.calls,
    tokens: point.tokens,
    cost: 0,
  }));

export const buildHourlyDistributionFromAnalytics = (
  points: MonitoringAnalyticsHourlyPoint[]
): MonitoringTimelinePoint[] => {
  const buckets = Array.from({ length: 24 }, (_, hour) => ({
    label: `${padNumber(hour)}:00`,
    requests: 0,
    tokens: 0,
    cost: 0,
  }));
  points.forEach((point) => {
    if (point.hour < 0 || point.hour > 23) return;
    buckets[point.hour] = {
      label: `${padNumber(point.hour)}:00`,
      requests: point.calls,
      tokens: point.tokens,
      cost: 0,
    };
  });
  return buckets;
};

export const buildModelShareRowsFromAnalytics = (
  rows: MonitoringAnalyticsModelShareRow[],
  modelStats: MonitoringAnalyticsModelStat[] = []
): MonitoringModelShareRow[] => {
  const successRateByModel = new Map(modelStats.map((row) => [row.model, row.success_rate]));
  return rows.map((row) => ({
    model: row.model,
    requests: row.calls,
    totalTokens: row.tokens,
    totalCost: row.cost,
    successRate: successRateByModel.get(row.model) ?? 1,
  }));
};

export const buildModelRowsFromAnalytics = (
  rows: MonitoringAnalyticsModelStat[]
): MonitoringModelRow[] =>
  rows.map((row) => ({
    model: row.model,
    requests: row.calls,
    failures: row.failure_calls,
    successRate: row.success_rate,
    totalTokens: row.total_tokens,
    totalCost: row.cost,
    averageLatencyMs: null,
    sources: 0,
    channels: 0,
  }));

const resolveChannelMeta = (
  authIndex: string,
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
) => {
  const authMeta = authMetaMap.get(authIndex);
  const channelMeta =
    channelByAuthIndex.get(authIndex) ||
    (authMeta?.authIndex ? channelByAuthIndex.get(authMeta.authIndex) : undefined);
  return { authMeta, channelMeta };
};

export const buildChannelRowsFromAnalytics = (
  rows: MonitoringAnalyticsChannelShareRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringChannelRow[] =>
  rows
    .map((row) => {
      const authIndex = row.auth_index || '-';
      const { authMeta, channelMeta } = resolveChannelMeta(
        authIndex,
        authMetaMap,
        channelByAuthIndex
      );
      const label = channelMeta?.name || authMeta?.provider || authIndex;
      return {
        id: authIndex,
        label,
        host: channelMeta?.host || '-',
        provider: authMeta?.provider || '-',
        planTypes: authMeta?.planType && authMeta.planType !== '-' ? [authMeta.planType] : [],
        disabled: channelMeta?.disabled || authMeta?.disabled || false,
        authCount: authIndex === '-' ? 0 : 1,
        modelCount: 0,
        requests: row.calls,
        failures: row.failure,
        successRate: row.calls > 0 ? row.success / row.calls : 1,
        totalTokens: row.tokens,
        totalCost: row.cost,
        averageLatencyMs: row.average_latency_ms,
        authLabels: authMeta?.label ? [authMeta.label] : [],
      } satisfies MonitoringChannelRow;
    })
    .sort((left, right) => right.requests - left.requests);

export const buildFailureSourceRowsFromAnalytics = (
  rows: MonitoringAnalyticsFailureSourceRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringFailureSourceRow[] =>
  rows.map((row) => {
    const { authMeta, channelMeta } = resolveChannelMeta(
      row.auth_index || '-',
      authMetaMap,
      channelByAuthIndex
    );
    return {
      id: `${row.source_hash || '-'}::${row.auth_index || '-'}`,
      label: shortHashLabel(row.source_hash),
      channel: channelMeta?.name || authMeta?.provider || row.auth_index || '-',
      failures: row.failure,
      totalRequests: row.calls,
      failureRate: row.calls > 0 ? row.failure / row.calls : 0,
      lastSeenAt: row.last_seen_ms,
      averageLatencyMs: row.average_latency_ms,
    };
  });

export const buildTaskBucketsFromAnalytics = (
  rows: MonitoringAnalyticsTaskBucketRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringTaskBucketRow[] =>
  rows.map((row) => {
    const authIndex = normalizeAuthIndex(row.auth_index) ?? '-';
    const authMeta = authMetaMap.get(authIndex);
    const sourceMeta = resolveSourceDisplay(row.source, authIndex, sourceInfoMap, authFileMap);
    const { channelMeta } = resolveChannelMeta(authIndex, authMetaMap, channelByAuthIndex);
    const sourceLabel =
      authMeta?.label || sourceMeta.displayName || shortHashLabel(row.source_hash);
    return {
      id: row.bucket_key,
      timestampMs: row.first_ms,
      timestamp: new Date(row.first_ms).toISOString(),
      source: sourceLabel,
      sourceMasked: maskEmailLike(sourceLabel),
      channel: channelMeta?.name || authMeta?.provider || sourceMeta.type || '-',
      authLabel: authMeta?.label || sourceLabel,
      planType: authMeta?.planType || '-',
      calls: row.total,
      failedCalls: row.failure,
      failed: row.failure > 0,
      modelsText: joinUnique(row.models, 3),
      totalTokens: row.total_tokens,
      cachedTokens: row.cached_tokens,
      cacheReadTokens: row.cache_read_tokens ?? 0,
      cacheCreationTokens: row.cache_creation_tokens ?? 0,
      totalCost: 0,
      averageLatencyMs: row.average_latency_ms,
      maxLatencyMs: row.max_latency_ms,
      endpointsText: joinUnique(row.endpoints, 2),
    };
  });

export const buildFailureRowsFromAnalytics = (
  rows: MonitoringAnalyticsRecentFailure[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringFailureRow[] =>
  rows.map((row) => {
    const authIndex = normalizeAuthIndex(row.auth_index) ?? '-';
    const { authMeta, channelMeta } = resolveChannelMeta(
      authIndex,
      authMetaMap,
      channelByAuthIndex
    );
    return {
      id: `${row.timestamp_ms}-${row.source_hash}-${row.api_key_hash}-${row.model}`,
      timestampMs: row.timestamp_ms,
      timestamp: new Date(row.timestamp_ms).toISOString(),
      model: row.model,
      source: shortHashLabel(row.source_hash || row.api_key_hash),
      channel: channelMeta?.name || authMeta?.provider || '-',
      authIndex: maskAuthIndex(authIndex),
      latencyMs: row.duration_ms,
    };
  });

const buildAnalyticsEventKey = (item: MonitoringAnalyticsEventRow) =>
  item.event_hash ||
  [
    item.timestamp_ms,
    item.model,
    item.source_hash,
    item.api_key_hash,
    item.auth_index,
    item.endpoint,
  ].join(':');

export const mergeAnalyticsEventItems = (
  previous: MonitoringAnalyticsEventRow[],
  next: MonitoringAnalyticsEventRow[]
) => {
  if (previous.length === 0) return next;
  const seen = new Set(previous.map(buildAnalyticsEventKey));
  const merged = previous.slice();
  next.forEach((item) => {
    const key = buildAnalyticsEventKey(item);
    if (seen.has(key)) return;
    seen.add(key);
    merged.push(item);
  });
  return merged;
};

export const buildUsageDetailsFromAnalyticsEvents = (
  items: MonitoringAnalyticsEventRow[] = []
): UsageDetailWithEndpoint[] =>
  items.map((item) => ({
    timestamp: new Date(item.timestamp_ms).toISOString(),
    source: readString(item.source),
    auth_index: item.auth_index || null,
    api_key_hash: readString(item.api_key_hash),
    account_snapshot: readString(item.account_snapshot),
    auth_label_snapshot: readString(item.auth_label_snapshot),
    auth_provider_snapshot: readString(item.auth_provider_snapshot),
    auth_project_id_snapshot: readString(item.auth_project_id_snapshot),
    reasoning_effort: readString(item.reasoning_effort),
    latency_ms: item.latency_ms ?? undefined,
    ttft_ms: item.ttft_ms ?? undefined,
    tokens: {
      input_tokens: item.input_tokens,
      output_tokens: item.output_tokens,
      reasoning_tokens: item.reasoning_tokens,
      cached_tokens: item.cached_tokens,
      cache_read_tokens: item.cache_read_tokens ?? 0,
      cache_creation_tokens: item.cache_creation_tokens ?? 0,
      total_tokens: item.total_tokens,
    },
    failed: item.failed === true,
    fail_status_code: item.fail_status_code ?? null,
    fail_summary: readString(item.fail_summary),
    __modelName: item.model,
    __resolvedModel: readString(item.resolved_model),
    __endpoint: item.endpoint || `${item.method} ${item.path}`.trim(),
    __endpointMethod: item.method,
    __endpointPath: item.path,
    __timestampMs: item.timestamp_ms,
  }));
