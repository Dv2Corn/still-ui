import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export interface TorrentBlockCounters {
  handshakes: number;
  dht: number;
  trackers: number;
  utp: number;
  learned: number;
  peerDrops: number;
  tunnelBans: number;
}

export interface TorrentBlockStatus {
  enabled: boolean;
  supported: boolean;
  nftInstalled: boolean;
  active: boolean;
  counters: TorrentBlockCounters;
  error?: string;
}

async function fetchTorrentBlock(): Promise<TorrentBlockStatus> {
  const msg = await HttpUtil.get<TorrentBlockStatus>('/panel/api/server/torrentBlock', undefined, {
    silent: true,
  });
  if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to read torrent blocking');
  return msg.obj;
}

export function useTorrentBlockQuery() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: keys.server.torrentBlock(),
    queryFn: fetchTorrentBlock,
  });
  const mutation = useMutation({
    mutationFn: (enable: boolean) =>
      HttpUtil.post<TorrentBlockStatus>('/panel/api/server/setTorrentBlock', {
        enable,
      }),
    onSuccess: (msg) => {
      if (msg?.success && msg.obj) queryClient.setQueryData(keys.server.torrentBlock(), msg.obj);
    },
  });
  return {
    status: query.data,
    setEnabled: mutation.mutate,
    saving: mutation.isPending,
  };
}

/** Which line explains the host half; empty when the filter is simply working or off. */
export function torrentBlockNotice(
  status: TorrentBlockStatus,
): 'unsupported' | 'nftMissing' | 'error' | '' {
  if (!status.enabled) return '';
  if (!status.supported) return 'unsupported';
  if (!status.nftInstalled) return 'nftMissing';
  if (status.error || !status.active) return 'error';
  return '';
}
