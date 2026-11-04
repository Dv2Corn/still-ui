import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { ReachHistoryPointSchema, ReachProbeViewSchema } from '@/generated/zod';
import type {
  ReachHistoryPoint,
  ReachProbeCreated,
  ReachProbeInput,
  ReachProbeView,
} from '@/generated/types';

export type { ReachHistoryPoint, ReachProbeCreated, ReachProbeInput, ReachProbeView };

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchProbes(): Promise<ReachProbeView[]> {
  const msg = await HttpUtil.get('/panel/api/probes/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch probes');
  const validated = parseMsg(msg, z.array(ReachProbeViewSchema), 'probes/list');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

async function fetchHistory(id: number): Promise<ReachHistoryPoint[]> {
  const msg = await HttpUtil.get(`/panel/api/probes/history/${id}`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch probe history');
  const validated = parseMsg(msg, z.array(ReachHistoryPointSchema), 'probes/history');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

// Results arrive once per probe interval (ten minutes by default), so a
// one-minute refresh keeps the page current without polling hard.
export function useReachProbesQuery() {
  return useQuery({ queryKey: keys.probes.list(), queryFn: fetchProbes, refetchInterval: 60_000 });
}

export function useReachProbeHistory(id: number | null) {
  return useQuery({
    queryKey: keys.probes.history(id ?? 0),
    queryFn: () => fetchHistory(id ?? 0),
    enabled: id != null,
  });
}

export function useReachProbeMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: keys.probes.root() });

  const create = useMutation({
    mutationFn: (input: ReachProbeInput) =>
      HttpUtil.post<ReachProbeCreated>('/panel/api/probes/add', input, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) return invalidate();
    },
  });
  const update = useMutation({
    mutationFn: ({ id, input }: { id: number; input: ReachProbeInput }) =>
      HttpUtil.post(`/panel/api/probes/update/${id}`, input, JSON_HEADERS),
    onSuccess: (msg) => {
      if (msg?.success) return invalidate();
    },
  });
  const remove = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/probes/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) return invalidate();
    },
  });
  const rotate = useMutation({
    mutationFn: (id: number) =>
      HttpUtil.post<{ token: string }>(`/panel/api/probes/rotateToken/${id}`, undefined, {
        silentSuccess: true,
      }),
  });

  return {
    create: (input: ReachProbeInput) => create.mutateAsync(input),
    update: (id: number, input: ReachProbeInput) => update.mutateAsync({ id, input }),
    remove: (id: number) => remove.mutateAsync(id),
    rotateToken: (id: number) => rotate.mutateAsync(id),
  };
}
