import { describe, expect, it } from 'vitest';
import type { MonitoringAnalyticsEventRow } from '@/services/api/usageService';
import { buildUsageDetailsFromAnalyticsEvents } from './analyticsAdapters';

describe('buildUsageDetailsFromAnalyticsEvents', () => {
  it('maps resolved model and auth project snapshots into usage details', () => {
    const events: MonitoringAnalyticsEventRow[] = [
      {
        event_hash: 'event-1',
        timestamp_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
        model: 'alias-model',
        resolved_model: 'upstream-model',
        endpoint: 'POST /v1/chat/completions',
        method: 'POST',
        path: '/v1/chat/completions',
        auth_index: 'auth-1',
        source: 'source.json',
        source_hash: 'source-hash',
        api_key_hash: 'api-key-hash',
        account_snapshot: 'account@example.com',
        auth_label_snapshot: 'label',
        auth_provider_snapshot: 'codex',
        auth_project_id_snapshot: 'project-1',
        reasoning_effort: 'medium',
        input_tokens: 10,
        output_tokens: 5,
        cached_tokens: 0,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
        reasoning_tokens: 1,
        total_tokens: 18,
        latency_ms: 123,
        ttft_ms: 45,
        failed: true,
        fail_status_code: 429,
        fail_summary: 'rate limit exceeded',
      },
    ];

    const details = buildUsageDetailsFromAnalyticsEvents(events);

    expect(details[0]).toMatchObject({
      __modelName: 'alias-model',
      __resolvedModel: 'upstream-model',
      auth_project_id_snapshot: 'project-1',
      reasoning_effort: 'medium',
      latency_ms: 123,
      ttft_ms: 45,
      tokens: {
        cached_tokens: 0,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
      },
      failed: true,
      fail_status_code: 429,
      fail_summary: 'rate limit exceeded',
    });
  });

  it('trusts backend-deduped cached tokens from analytics events', () => {
    const events: MonitoringAnalyticsEventRow[] = [
      {
        event_hash: 'event-cache',
        timestamp_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
        model: 'mixed-cache-model',
        endpoint: 'POST /v1/chat/completions',
        method: 'POST',
        path: '/v1/chat/completions',
        auth_index: 'auth-1',
        source: 'source.json',
        source_hash: 'source-hash',
        api_key_hash: 'api-key-hash',
        account_snapshot: '',
        auth_label_snapshot: '',
        auth_provider_snapshot: '',
        input_tokens: 100,
        output_tokens: 20,
        cached_tokens: 5,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
        reasoning_tokens: 0,
        total_tokens: 130,
        latency_ms: null,
        failed: false,
      },
    ];

    const details = buildUsageDetailsFromAnalyticsEvents(events);

    expect(details[0].tokens.cached_tokens).toBe(5);
    expect(details[0].tokens.cache_read_tokens).toBe(4);
    expect(details[0].tokens.cache_creation_tokens).toBe(1);
  });
});
