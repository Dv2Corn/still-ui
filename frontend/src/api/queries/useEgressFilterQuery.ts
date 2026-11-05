import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export type EgressNodeScope = '' | 'all' | 'selected';

export interface EgressFilterSettings {
  smtp: boolean;
  mailSubmission: boolean;
  private: boolean;
  server: boolean;
  custom: string[] | null;
  nodeScope: EgressNodeScope;
  nodeIds: number[] | null;
  /** panelGuid of the central panel that set this filter; empty when it is this panel's own. */
  managedBy?: string;
  managedAt?: number;
}

export interface EgressFilterCounters {
  mail: number;
  private: number;
  server: number;
  custom: number;
}

export interface EgressFilterNodeState {
  id: number;
  name: string;
  wanted: 'apply' | 'release';
  synced: boolean;
  error?: string;
  at?: number;
}

export interface EgressFilterStatus {
  settings: EgressFilterSettings;
  host: {
    supported: boolean;
    nftInstalled: boolean;
    active: boolean;
    counters: EgressFilterCounters;
  };
  hostNeeded: boolean;
  error?: string;
  serverPorts: number[] | null;
  templatePrivateBlocked: boolean;
  nodes: EgressFilterNodeState[] | null;
}

export type EgressFilterInput = Pick<
  EgressFilterSettings,
  'smtp' | 'mailSubmission' | 'private' | 'server' | 'custom' | 'nodeScope' | 'nodeIds'
>;

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchEgressFilter(): Promise<EgressFilterStatus> {
  const msg = await HttpUtil.get<EgressFilterStatus>('/panel/api/server/egressFilter', undefined, {
    silent: true,
  });
  if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to read the egress filter');
  return msg.obj;
}

export function useEgressFilterQuery() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: keys.server.egressFilter(),
    queryFn: fetchEgressFilter,
  });
  const mutation = useMutation({
    mutationFn: (input: EgressFilterInput) =>
      HttpUtil.post<EgressFilterStatus>('/panel/api/server/setEgressFilter', input, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success && msg.obj) queryClient.setQueryData(keys.server.egressFilter(), msg.obj);
    },
  });
  return {
    status: query.data,
    save: mutation.mutateAsync,
    saving: mutation.isPending,
  };
}

/** Which line explains the nftables half; empty when it is working or not needed. */
export function egressHostNotice(
  status: EgressFilterStatus,
): 'unsupported' | 'nftMissing' | 'error' | '' {
  if (!status.hostNeeded) return '';
  if (!status.host.supported) return 'unsupported';
  if (!status.host.nftInstalled) return 'nftMissing';
  if (status.error || !status.host.active) return 'error';
  return '';
}

/** Splits what the operator typed into entries; the server validates them. */
export function splitCustomEntries(values: string[]): string[] {
  return values
    .flatMap((v) => v.split(/[\s,;]+/))
    .map((v) => v.trim())
    .filter(Boolean);
}
