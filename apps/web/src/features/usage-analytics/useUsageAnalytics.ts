import { useCallback, useEffect, useMemo, useState } from 'react';
import { useMonitoringAnalytics } from '@/features/monitoring/hooks/useMonitoringAnalytics';
import {
  adaptUsageAnalyticsData,
  analyzeUsageBucket,
  buildCredentialQuotaRows,
  buildEntityTrendSeries,
  buildKeyAnomalies,
  buildUsageInsights,
  buildUsageMatrix,
  buildUsageSummaryDelta,
  buildUsageAnalyticsFilters,
  buildUsageAnalyticsInclude,
  getUsageRangeBounds,
  resolveUsageGranularity,
  USAGE_ANALYTICS_DEFAULT_FILTERS,
  type UsageMatrixDimension,
  type UsageMatrixMetricKey,
  type UsageTrendMetricKey,
  type UsageAnalyticsFiltersState,
  type UsageAnalyticsTab,
  type UsageSelectedFilterKey,
  type UsageAnomalyAnalysis,
  type UsageTimelinePoint,
} from './usageAnalyticsModel';
import {
  readUsageAnalyticsUiState,
  writeUsageAnalyticsUiState,
} from './usageAnalyticsUiState';

const USAGE_SEARCH_DEBOUNCE_MS = 350;

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debouncedValue, setDebouncedValue] = useState(value);

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedValue(value), delayMs);
    return () => clearTimeout(timer);
  }, [delayMs, value]);

  return debouncedValue;
}

export function useUsageAnalytics() {
  const [filters, setFiltersState] = useState<UsageAnalyticsFiltersState>(
    () => readUsageAnalyticsUiState().filters
  );
  const [activeTabState, setActiveTabState] = useState<UsageAnalyticsTab>('overview');
  const [nowMs, setNowMs] = useState(() => Date.now());
  const [selectedBucketMs, setSelectedBucketMs] = useState<number | null>(null);
  const [selectedModelId, setSelectedModelId] = useState('');
  const [selectedApiKeyHash, setSelectedApiKeyHash] = useState('');
  const [selectedCredentialId, setSelectedCredentialId] = useState('');
  const [trendMetric, setTrendMetric] = useState<UsageTrendMetricKey>('requestCount');
  const [matrixDimension, setMatrixDimension] =
    useState<UsageMatrixDimension>('apiKeyModel');
  const [matrixMetric, setMatrixMetric] = useState<UsageMatrixMetricKey>('requestCount');
  const [activeCredentialsOnly, setActiveCredentialsOnly] = useState(true);
  const setActiveTab = useCallback((tab: UsageAnalyticsTab) => {
    setActiveTabState(tab);
  }, []);
  const debouncedSearchQuery = useDebouncedValue(
    filters.searchQuery.trim(),
    USAGE_SEARCH_DEBOUNCE_MS
  );

  const bounds = useMemo(() => getUsageRangeBounds(filters, nowMs), [filters, nowMs]);
  const resolvedGranularity = useMemo(
    () => resolveUsageGranularity(filters, nowMs),
    [filters, nowMs]
  );
  const analyticsFilters = useMemo(
    () => buildUsageAnalyticsFilters(filters),
    [filters]
  );
  const drilldownPreview = useMemo(() => {
    if (selectedBucketMs === null) return null;
    return {
      fromMs: selectedBucketMs,
      toMs:
        selectedBucketMs +
        (resolvedGranularity === 'day' ? 24 * 60 * 60 * 1000 : 60 * 60 * 1000),
      limit: 12,
    };
  }, [resolvedGranularity, selectedBucketMs]);
  const include = useMemo(
    () => buildUsageAnalyticsInclude(resolvedGranularity, drilldownPreview),
    [drilldownPreview, resolvedGranularity]
  );
  const dataScopeKey = useMemo(
    () =>
      JSON.stringify({
        bounds,
        drilldownPreview,
        filters: analyticsFilters,
        granularity: resolvedGranularity,
        searchQuery: debouncedSearchQuery,
      }),
    [analyticsFilters, bounds, debouncedSearchQuery, drilldownPreview, resolvedGranularity]
  );

  const analytics = useMonitoringAnalytics({
    fromMs: bounds?.fromMs,
    toMs: bounds?.toMs,
    nowMs,
    dataScopeKey,
    searchQuery: debouncedSearchQuery,
    filters: analyticsFilters,
    include,
    throttleMs: 0,
  });

  const analyticsData = analytics.dataStale ? null : analytics.data;
  const adapted = useMemo(
    () => adaptUsageAnalyticsData(analyticsData, resolvedGranularity, filters.apiKeyKeyword),
    [analyticsData, filters.apiKeyKeyword, resolvedGranularity]
  );

  const summaryDelta = useMemo(
    () => buildUsageSummaryDelta(adapted.summary, adapted.summaryComparison),
    [adapted.summary, adapted.summaryComparison]
  );

  const selectedBucket = useMemo(
    () =>
      selectedBucketMs === null
        ? null
        : (adapted.timeline.find((point) => point.bucketMs === selectedBucketMs) ?? null),
    [adapted.timeline, selectedBucketMs]
  );

  const anomalyAnalysis = useMemo<UsageAnomalyAnalysis | null>(
    () =>
      selectedBucketMs === null ? null : analyzeUsageBucket(adapted.timeline, selectedBucketMs),
    [adapted.timeline, selectedBucketMs]
  );

  const selectedModel =
    adapted.modelRows.find((row) => row.id === selectedModelId) ?? adapted.modelRows[0] ?? null;
  const selectedApiKey =
    adapted.apiKeyRows.find((row) => row.apiKeyHash === selectedApiKeyHash) ??
    adapted.apiKeyRows[0] ??
    null;
  const selectedCredential =
    adapted.credentialRows.find((row) => row.id === selectedCredentialId) ??
    adapted.credentialRows[0] ??
    null;

  const visibleCredentialRows = useMemo(
    () =>
      activeCredentialsOnly
        ? adapted.credentialRows.filter((row) => row.requestCount > 0)
        : adapted.credentialRows,
    [activeCredentialsOnly, adapted.credentialRows]
  );

  const modelTrendSeries = useMemo(
    () => buildEntityTrendSeries(adapted.modelRows, adapted.timeline, trendMetric, 4),
    [adapted.modelRows, adapted.timeline, trendMetric]
  );
  const apiKeyTrendSeries = useMemo(
    () => buildEntityTrendSeries(adapted.apiKeyRows, adapted.timeline, trendMetric, 4),
    [adapted.apiKeyRows, adapted.timeline, trendMetric]
  );
  const matrix = useMemo(
    () =>
      buildUsageMatrix({
        apiKeyRows: adapted.apiKeyRows,
        credentialRows: adapted.credentialRows,
        dimension: matrixDimension,
        metric: matrixMetric,
      }),
    [adapted.apiKeyRows, adapted.credentialRows, matrixDimension, matrixMetric]
  );
  const keyAnomalies = useMemo(
    () => buildKeyAnomalies(adapted.apiKeyRows),
    [adapted.apiKeyRows]
  );
  const credentialQuotaRows = useMemo(
    () => buildCredentialQuotaRows(visibleCredentialRows, nowMs),
    [nowMs, visibleCredentialRows]
  );
  const insights = useMemo(
    () =>
      buildUsageInsights({
        apiKeyRows: adapted.apiKeyRows,
        credentialRows: adapted.credentialRows,
        modelRows: adapted.modelRows,
        providerRows: adapted.providerRows,
        summary: adapted.summary,
      }),
    [
      adapted.apiKeyRows,
      adapted.credentialRows,
      adapted.modelRows,
      adapted.providerRows,
      adapted.summary,
    ]
  );
  const setFilters = useCallback((patch: Partial<UsageAnalyticsFiltersState>) => {
    setFiltersState((current) => {
      const next = { ...current, ...patch };
      writeUsageAnalyticsUiState({ filters: next });
      return next;
    });
    setSelectedBucketMs(null);
  }, []);

  const resetFilters = useCallback(() => {
    setFiltersState(USAGE_ANALYTICS_DEFAULT_FILTERS);
    writeUsageAnalyticsUiState({ filters: USAGE_ANALYTICS_DEFAULT_FILTERS });
    setSelectedBucketMs(null);
  }, []);

  const clearFilter = useCallback((key: UsageSelectedFilterKey) => {
    setFiltersState((current) => {
      const next = {
        ...current,
        [key]: 'all',
      };
      writeUsageAnalyticsUiState({ filters: next });
      return next;
    });
    setSelectedBucketMs(null);
  }, []);

  const selectBucket = useCallback((point: UsageTimelinePoint | null) => {
    setSelectedBucketMs(point?.bucketMs ?? null);
  }, []);

  const refresh = useCallback(() => {
    setNowMs(Date.now());
    void analytics.refresh({ force: true });
  }, [analytics]);

  return {
    filters,
    setFilters,
    resetFilters,
    clearFilter,
    activeTab: activeTabState,
    setActiveTab,
    bounds,
    resolvedGranularity,
    loading: analytics.loading,
    error: analytics.error,
    enabled: analytics.enabled,
    unavailableReason: analytics.unavailableReason,
    lastRefreshedAt: analytics.lastRefreshedAt,
    refresh,
    summary: adapted.summary,
    summaryDelta,
    timeline: adapted.timeline,
    modelRows: adapted.modelRows,
    apiKeyRows: adapted.apiKeyRows,
    credentialRows: visibleCredentialRows,
    allCredentialRows: adapted.credentialRows,
    providerRows: adapted.providerRows,
    heatmap: adapted.heatmap,
    matrix,
    matrixDimension,
    setMatrixDimension,
    matrixMetric,
    setMatrixMetric,
    trendMetric,
    setTrendMetric,
    modelTrendSeries,
    apiKeyTrendSeries,
    keyAnomalies,
    credentialQuotaRows,
    activeCredentialsOnly,
    setActiveCredentialsOnly,
    insights,
    anomalyPoints: adapted.anomalyPoints,
    drilldownPreview: adapted.drilldownPreview,
    filterOptions: adapted.filterOptions,
    selectedBucket,
    selectBucket,
    anomalyAnalysis,
    selectedModel,
    setSelectedModelId,
    selectedApiKey,
    setSelectedApiKeyHash,
    selectedCredential,
    setSelectedCredentialId,
  };
}
