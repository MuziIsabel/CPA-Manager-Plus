import { useCallback, useEffect, useRef, useState } from 'react';
import { resolveUsageServiceBase } from '@/entities/usageService/baseResolver';
import {
  usageServiceApi,
  type ApiKeyAlias,
  type ApiKeyAliasesResponse,
  type ModelPricesResponse,
  type ModelPriceSyncResponse,
  type UsageExportResponse,
  type UsageImportResponse,
} from '@/services/api/usageService';
import { useAuthStore, useUsageServiceStore } from '@/stores';
import { clearModelPrices, loadModelPrices, saveModelPrices, type ModelPrice } from '@/utils/usage';

export interface UsagePayload {
  total_requests?: number;
  success_count?: number;
  failure_count?: number;
  total_tokens?: number;
  apis?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface UseUsageDataReturn {
  usage: UsagePayload | null;
  loading: boolean;
  error: string;
  lastRefreshedAt: Date | null;
  modelPrices: Record<string, ModelPrice>;
  apiKeyAliases: ApiKeyAlias[];
  usageServiceAvailable: boolean;
  setModelPrices: (prices: Record<string, ModelPrice>) => Promise<void>;
  loadApiKeyAliases: () => Promise<void>;
  syncModelPrices: (models?: string[]) => Promise<ModelPriceSyncResponse>;
  exportUsage: () => Promise<UsageExportResponse>;
  importUsage: (file: File) => Promise<UsageImportResponse>;
  loadUsage: () => Promise<void>;
}

export interface UseUsageDataOptions {
  loadUsageEvents?: boolean;
}

export function useUsageData({
  loadUsageEvents = true,
}: UseUsageDataOptions = {}): UseUsageDataReturn {
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);
  const [usage, setUsage] = useState<UsagePayload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const [modelPrices, setModelPricesState] = useState<Record<string, ModelPrice>>({});
  const [apiKeyAliases, setApiKeyAliases] = useState<ApiKeyAlias[]>([]);
  const [usageServiceAvailable, setUsageServiceAvailable] = useState(false);
  const requestIdRef = useRef(0);
  const aliasRequestIdRef = useRef(0);

  const resolveServiceBase = useCallback(
    () =>
      resolveUsageServiceBase({
        apiBase,
        usageServiceEnabled,
        usageServiceBase,
      }),
    [apiBase, usageServiceBase, usageServiceEnabled]
  );

  const getModelPricesFromApi = useCallback(async (): Promise<ModelPricesResponse> => {
    const serviceBase = await resolveServiceBase();
    if (!serviceBase) {
      return { prices: {} };
    }
    return usageServiceApi.getModelPrices(serviceBase, managementKey);
  }, [managementKey, resolveServiceBase]);

  const getApiKeyAliasesFromApi = useCallback(async (): Promise<ApiKeyAliasesResponse> => {
    const serviceBase = await resolveServiceBase();
    if (!serviceBase) {
      return { items: [] };
    }
    return usageServiceApi.getApiKeyAliases(serviceBase, managementKey);
  }, [managementKey, resolveServiceBase]);

  const saveModelPricesToApi = useCallback(
    async (prices: Record<string, ModelPrice>): Promise<ModelPricesResponse> => {
      const serviceBase = await resolveServiceBase();
      if (!serviceBase) {
        throw new Error('model_price_api_unavailable');
      }
      return usageServiceApi.saveModelPrices(serviceBase, prices, managementKey);
    },
    [managementKey, resolveServiceBase]
  );

  const syncModelPricesFromApi = useCallback(
    async (models?: string[]): Promise<ModelPriceSyncResponse> => {
      const serviceBase = await resolveServiceBase();
      if (!serviceBase) {
        throw new Error('model_price_sync_requires_usage_service');
      }
      return usageServiceApi.syncModelPrices(serviceBase, managementKey, models);
    },
    [managementKey, resolveServiceBase]
  );

  const exportUsageFromApi = useCallback(async (): Promise<UsageExportResponse> => {
    const serviceBase = await resolveServiceBase();
    if (!serviceBase) {
      throw new Error('usage_import_export_requires_usage_service');
    }
    return usageServiceApi.exportUsage(serviceBase, managementKey);
  }, [managementKey, resolveServiceBase]);

  const importUsageToApi = useCallback(
    async (file: File): Promise<UsageImportResponse> => {
      const serviceBase = await resolveServiceBase();
      if (!serviceBase) {
        throw new Error('usage_import_export_requires_usage_service');
      }
      return usageServiceApi.importUsage(serviceBase, file, managementKey);
    },
    [managementKey, resolveServiceBase]
  );

  const loadModelPricesFromStorage = useCallback(async () => {
    const fallbackPrices = loadModelPrices();
    try {
      const response = await getModelPricesFromApi();
      const apiPrices = response.prices ?? {};
      if (Object.keys(apiPrices).length > 0) {
        setModelPricesState(apiPrices);
        clearModelPrices();
        return;
      }
      if (Object.keys(fallbackPrices).length > 0) {
        const migrated = await saveModelPricesToApi(fallbackPrices);
        setModelPricesState(migrated.prices ?? fallbackPrices);
        clearModelPrices();
        return;
      }
      setModelPricesState({});
    } catch {
      setModelPricesState(fallbackPrices);
    }
  }, [getModelPricesFromApi, saveModelPricesToApi]);

  const loadApiKeyAliases = useCallback(async () => {
    const requestId = aliasRequestIdRef.current + 1;
    aliasRequestIdRef.current = requestId;
    try {
      const response = await getApiKeyAliasesFromApi();
      if (aliasRequestIdRef.current !== requestId) return;
      setApiKeyAliases(Array.isArray(response.items) ? response.items : []);
    } catch {
      if (aliasRequestIdRef.current !== requestId) return;
      setApiKeyAliases([]);
    }
  }, [getApiKeyAliasesFromApi]);

  const loadUsage = useCallback(async () => {
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    if (!loadUsageEvents) {
      setUsageServiceAvailable(false);
      setUsage(null);
      setLastRefreshedAt(null);
      setLoading(false);
      setError('');
      return;
    }

    setLoading(true);
    setError('');

    try {
      const serviceBase = await resolveServiceBase();
      if (!serviceBase) {
        setUsageServiceAvailable(false);
        setUsage(null);
        setLastRefreshedAt(null);
        return;
      }
      setUsageServiceAvailable(true);
      const payload = await usageServiceApi.getUsage(serviceBase, managementKey);
      if (requestIdRef.current !== requestId) return;
      setUsage(payload ?? null);
      setLastRefreshedAt(new Date());
    } catch (err) {
      if (requestIdRef.current !== requestId) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (requestIdRef.current === requestId) {
        setLoading(false);
      }
    }
  }, [loadUsageEvents, managementKey, resolveServiceBase]);

  useEffect(() => {
    void loadModelPricesFromStorage();
    void loadApiKeyAliases();
    void loadUsage();
  }, [loadApiKeyAliases, loadModelPricesFromStorage, loadUsage]);

  const setModelPrices = useCallback(
    async (prices: Record<string, ModelPrice>) => {
      setModelPricesState(prices);
      try {
        const response = await saveModelPricesToApi(prices);
        setModelPricesState(response.prices ?? prices);
        clearModelPrices();
      } catch {
        saveModelPrices(prices);
      }
    },
    [saveModelPricesToApi]
  );

  const syncModelPrices = useCallback(
    async (models?: string[]) => {
      const response = await syncModelPricesFromApi(models);
      setModelPricesState(response.prices ?? {});
      clearModelPrices();
      return response;
    },
    [syncModelPricesFromApi]
  );

  return {
    usage,
    loading,
    error,
    lastRefreshedAt,
    modelPrices,
    apiKeyAliases,
    usageServiceAvailable,
    setModelPrices,
    loadApiKeyAliases,
    syncModelPrices,
    exportUsage: exportUsageFromApi,
    importUsage: importUsageToApi,
    loadUsage,
  };
}
