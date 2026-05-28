import { describe, expect, it } from 'vitest';
import type { UsageDetailWithEndpoint } from '@/utils/usage';
import { buildEventRows } from './eventRows';

const buildRows = (overrides: Partial<UsageDetailWithEndpoint> = {}) =>
  buildEventRows(
    [
      {
        timestamp: '2026-05-19T10:00:00Z',
        source: 'alice@example.com',
        auth_index: 'auth-1',
        latency_ms: 1500,
        ttft_ms: 500,
        tokens: {
          input_tokens: 10,
          output_tokens: 20,
          total_tokens: 30,
        },
        failed: false,
        __modelName: 'gpt-5.4',
        __endpoint: 'POST /v1/chat/completions',
        __endpointMethod: 'POST',
        __endpointPath: '/v1/chat/completions',
        __timestampMs: Date.parse('2026-05-19T10:00:00Z'),
        ...overrides,
      },
    ],
    new Map(),
    new Map(),
    { byAuthIndex: new Map(), bySource: new Map() },
    new Map(),
    {},
    new Map()
  );

describe('buildEventRows', () => {
  it('calculates output tokens per second after TTFT when available', () => {
    const [row] = buildRows();

    expect(row.latencyMs).toBe(1500);
    expect(row.ttftMs).toBe(500);
    expect(row.tokensPerSecond).toBe(20);
  });

  it('falls back to total latency when TTFT is missing or not smaller than total latency', () => {
    const [withoutTTFT] = buildRows({ ttft_ms: undefined });
    const [invalidTTFT] = buildRows({ ttft_ms: 2000 });

    expect(withoutTTFT.tokensPerSecond).toBeCloseTo(20 / 1.5);
    expect(invalidTTFT.tokensPerSecond).toBeCloseTo(20 / 1.5);
  });
});
