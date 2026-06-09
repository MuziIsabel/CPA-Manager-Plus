import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { USAGE_ANALYTICS_DEFAULT_FILTERS } from './usageAnalyticsModel';
import {
  USAGE_ANALYTICS_UI_STATE_STORAGE_KEY,
  getDefaultUsageAnalyticsUiState,
  normalizeUsageAnalyticsFilters,
  normalizeUsageAnalyticsUiState,
  readUsageAnalyticsUiState,
  writeUsageAnalyticsUiState,
} from './usageAnalyticsUiState';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
  removeItem: (key: string) => void;
  clear: () => void;
};

const createMemoryStorage = (): StorageLike => {
  const store = new Map<string, string>();
  return {
    getItem: (key) => (store.has(key) ? (store.get(key) as string) : null),
    setItem: (key, value) => {
      store.set(key, value);
    },
    removeItem: (key) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
};

const originalWindow = (globalThis as { window?: unknown }).window;

describe('usageAnalyticsUiState', () => {
  let storage: StorageLike;

  beforeEach(() => {
    storage = createMemoryStorage();
    (globalThis as { window?: unknown }).window = { localStorage: storage };
  });

  afterEach(() => {
    if (originalWindow === undefined) {
      delete (globalThis as { window?: unknown }).window;
    } else {
      (globalThis as { window?: unknown }).window = originalWindow;
    }
  });

  it('uses 24h defaults when storage is empty', () => {
    expect(getDefaultUsageAnalyticsUiState()).toEqual({
      filters: USAGE_ANALYTICS_DEFAULT_FILTERS,
    });
    expect(readUsageAnalyticsUiState()).toEqual(getDefaultUsageAnalyticsUiState());
    expect(readUsageAnalyticsUiState().filters.timeRange).toBe('24h');
  });

  it('normalizes persisted filters and ignores removed fields', () => {
    const filters = normalizeUsageAnalyticsFilters({
      timeRange: 'custom',
      customRange: { startMs: 1_000, endMs: 2_000 },
      granularity: 'day',
      model: ' gpt-4o ',
      apiKeyHash: ' hash ',
      provider: '',
      authFile: 'auth.json',
      status: 'failed',
      searchQuery: 'req-42',
      minLatencyMs: '10000',
      cacheStatus: 'hit',
      apiKeyKeyword: 'key',
      requestType: 'codex',
      projectId: 'project-a',
      excludeZeroToken: true,
    });

    expect(filters).toEqual({
      ...USAGE_ANALYTICS_DEFAULT_FILTERS,
      timeRange: 'custom',
      customRange: { startMs: 1_000, endMs: 2_000 },
      granularity: 'day',
      model: 'gpt-4o',
      apiKeyHash: 'hash',
      provider: 'all',
      authFile: 'auth.json',
      status: 'failed',
      searchQuery: 'req-42',
      minLatencyMs: '10000',
      cacheStatus: 'hit',
      apiKeyKeyword: 'key',
    });
    expect('requestType' in filters).toBe(false);
    expect('projectId' in filters).toBe(false);
    expect('excludeZeroToken' in filters).toBe(false);
  });

  it('falls back to safe defaults for dirty persisted values', () => {
    expect(
      normalizeUsageAnalyticsUiState({
        filters: {
          timeRange: 'custom',
          customRange: { startMs: 3_000, endMs: 2_000 },
          granularity: 'bad',
          status: 'bad',
          minLatencyMs: '1',
          cacheStatus: 'read',
          model: '',
          searchQuery: 42,
        },
      })
    ).toEqual({
      filters: {
        ...USAGE_ANALYTICS_DEFAULT_FILTERS,
        model: 'all',
      },
    });
  });

  it('persists and reads filters via localStorage', () => {
    writeUsageAnalyticsUiState({
      filters: {
        ...USAGE_ANALYTICS_DEFAULT_FILTERS,
        timeRange: '7d',
        model: 'gpt-4o',
        cacheStatus: 'miss',
      },
    });

    expect(JSON.parse(storage.getItem(USAGE_ANALYTICS_UI_STATE_STORAGE_KEY) ?? '{}')).toEqual({
      filters: {
        ...USAGE_ANALYTICS_DEFAULT_FILTERS,
        timeRange: '7d',
        model: 'gpt-4o',
        cacheStatus: 'miss',
      },
    });
    expect(readUsageAnalyticsUiState()).toEqual({
      filters: {
        ...USAGE_ANALYTICS_DEFAULT_FILTERS,
        timeRange: '7d',
        model: 'gpt-4o',
        cacheStatus: 'miss',
      },
    });
  });

  it('returns defaults when stored payload is invalid JSON', () => {
    storage.setItem(USAGE_ANALYTICS_UI_STATE_STORAGE_KEY, '{not json');
    expect(readUsageAnalyticsUiState()).toEqual(getDefaultUsageAnalyticsUiState());
  });
});
