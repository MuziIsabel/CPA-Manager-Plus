import {
  USAGE_ANALYTICS_DEFAULT_FILTERS,
  type UsageAnalyticsCacheStatus,
  type UsageAnalyticsCustomRange,
  type UsageAnalyticsFiltersState,
  type UsageAnalyticsGranularity,
  type UsageAnalyticsLatencyFilter,
  type UsageAnalyticsStatus,
  type UsageAnalyticsTimeRange,
} from './usageAnalyticsModel';

export const USAGE_ANALYTICS_UI_STATE_STORAGE_KEY = 'usageAnalytics.uiState';

export type UsageAnalyticsUiState = {
  filters: UsageAnalyticsFiltersState;
};

const TIME_RANGE_SET = new Set<UsageAnalyticsTimeRange>([
  '24h',
  'today',
  'yesterday',
  '7d',
  '30d',
  'custom',
]);
const GRANULARITY_SET = new Set<UsageAnalyticsGranularity>(['auto', 'hour', 'day']);
const STATUS_SET = new Set<UsageAnalyticsStatus>(['all', 'success', 'failed']);
const LATENCY_SET = new Set<UsageAnalyticsLatencyFilter>(['all', '3000', '10000', '30000']);
const CACHE_STATUS_SET = new Set<UsageAnalyticsCacheStatus>(['all', 'hit', 'miss']);

const normalizeTimeRange = (value: unknown): UsageAnalyticsTimeRange =>
  typeof value === 'string' && TIME_RANGE_SET.has(value as UsageAnalyticsTimeRange)
    ? (value as UsageAnalyticsTimeRange)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.timeRange;

const normalizeGranularity = (value: unknown): UsageAnalyticsGranularity =>
  typeof value === 'string' && GRANULARITY_SET.has(value as UsageAnalyticsGranularity)
    ? (value as UsageAnalyticsGranularity)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.granularity;

const normalizeStatus = (value: unknown): UsageAnalyticsStatus =>
  typeof value === 'string' && STATUS_SET.has(value as UsageAnalyticsStatus)
    ? (value as UsageAnalyticsStatus)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.status;

const normalizeLatency = (value: unknown): UsageAnalyticsLatencyFilter =>
  typeof value === 'string' && LATENCY_SET.has(value as UsageAnalyticsLatencyFilter)
    ? (value as UsageAnalyticsLatencyFilter)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.minLatencyMs;

const normalizeCacheStatus = (value: unknown): UsageAnalyticsCacheStatus =>
  typeof value === 'string' && CACHE_STATUS_SET.has(value as UsageAnalyticsCacheStatus)
    ? (value as UsageAnalyticsCacheStatus)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.cacheStatus;

const normalizeSelectValue = (value: unknown): string => {
  const normalized = typeof value === 'string' ? value.trim() : '';
  return normalized || 'all';
};

const normalizeInputValue = (value: unknown): string =>
  typeof value === 'string' ? value : '';

const normalizeCustomRange = (value: unknown): UsageAnalyticsCustomRange | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;

  const record = value as Record<string, unknown>;
  const startMs = record.startMs;
  const endMs = record.endMs;
  if (
    typeof startMs !== 'number' ||
    typeof endMs !== 'number' ||
    !Number.isFinite(startMs) ||
    !Number.isFinite(endMs) ||
    startMs >= endMs
  ) {
    return null;
  }
  return { startMs, endMs };
};

export const getDefaultUsageAnalyticsUiState = (): UsageAnalyticsUiState => ({
  filters: USAGE_ANALYTICS_DEFAULT_FILTERS,
});

export const normalizeUsageAnalyticsFilters = (value: unknown): UsageAnalyticsFiltersState => {
  const defaults = USAGE_ANALYTICS_DEFAULT_FILTERS;
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return defaults;
  }

  const record = value as Record<string, unknown>;
  const customRange = normalizeCustomRange(record.customRange);
  const timeRange = normalizeTimeRange(record.timeRange);
  return {
    timeRange: timeRange === 'custom' && !customRange ? defaults.timeRange : timeRange,
    customRange,
    granularity: normalizeGranularity(record.granularity),
    model: normalizeSelectValue(record.model),
    apiKeyHash: normalizeSelectValue(record.apiKeyHash),
    provider: normalizeSelectValue(record.provider),
    authFile: normalizeSelectValue(record.authFile),
    status: normalizeStatus(record.status),
    searchQuery: normalizeInputValue(record.searchQuery),
    minLatencyMs: normalizeLatency(record.minLatencyMs),
    cacheStatus: normalizeCacheStatus(record.cacheStatus),
    apiKeyKeyword: normalizeInputValue(record.apiKeyKeyword),
  };
};

export const normalizeUsageAnalyticsUiState = (value: unknown): UsageAnalyticsUiState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return getDefaultUsageAnalyticsUiState();
  }

  return {
    filters: normalizeUsageAnalyticsFilters((value as Record<string, unknown>).filters),
  };
};

export const readUsageAnalyticsUiState = (): UsageAnalyticsUiState => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return getDefaultUsageAnalyticsUiState();
  }

  try {
    const raw = window.localStorage.getItem(USAGE_ANALYTICS_UI_STATE_STORAGE_KEY);
    if (raw) {
      return normalizeUsageAnalyticsUiState(JSON.parse(raw));
    }
  } catch {
    // Ignore storage failures and fall back to defaults.
  }

  return getDefaultUsageAnalyticsUiState();
};

export const writeUsageAnalyticsUiState = (state: Partial<UsageAnalyticsUiState>) => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') return;

  try {
    window.localStorage.setItem(
      USAGE_ANALYTICS_UI_STATE_STORAGE_KEY,
      JSON.stringify(normalizeUsageAnalyticsUiState(state))
    );
  } catch {
    // Ignore storage failures and keep the runtime state in memory only.
  }
};
