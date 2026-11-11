import { useQuery } from '@tanstack/react-query';

import { PortHopStatusSchema, type PortHopStatus } from '@/generated/zod';
import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

async function fetchPortHop(): Promise<PortHopStatus> {
  const msg = await HttpUtil.get('/panel/api/server/portHop', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to read the port hopping status');
  return PortHopStatusSchema.parse(msg.obj);
}

// The panel converges the host every 10 seconds, so a saved inbound shows up on the next poll.
export function usePortHopQuery(enabled: boolean) {
  return useQuery({
    queryKey: keys.server.portHop(),
    queryFn: fetchPortHop,
    enabled,
    refetchInterval: 15_000,
  });
}
