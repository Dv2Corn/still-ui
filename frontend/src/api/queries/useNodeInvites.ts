import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { NodeInviteViewSchema } from '@/generated/zod';
import type { NodeInviteCreated, NodeInviteInput, NodeInviteView } from '@/generated/types';

export type { NodeInviteCreated, NodeInviteInput, NodeInviteView };

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchInvites(): Promise<NodeInviteView[]> {
  const msg = await HttpUtil.get('/panel/api/nodes/invites', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch node invitations');
  const validated = parseMsg(msg, z.array(NodeInviteViewSchema), 'nodes/invites');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

// A pending invitation turns "used" when its node joins, so the list refreshes
// while the dialog is open.
export function useNodeInvitesQuery(enabled: boolean) {
  return useQuery({
    queryKey: keys.nodes.invites(),
    queryFn: fetchInvites,
    enabled,
    refetchInterval: enabled ? 5_000 : false,
  });
}

export function useNodeInviteMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: keys.nodes.root() });

  const create = useMutation({
    mutationFn: (input: NodeInviteInput) =>
      HttpUtil.post<NodeInviteCreated>('/panel/api/nodes/invites/create', input, {
        ...JSON_HEADERS,
        silentSuccess: true,
      }),
    onSuccess: (msg) => {
      if (msg?.success) return invalidate();
    },
  });
  const revoke = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/nodes/invites/revoke/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) return invalidate();
    },
  });

  return {
    create: (input: NodeInviteInput) => create.mutateAsync(input),
    revoke: (id: number) => revoke.mutateAsync(id),
  };
}
