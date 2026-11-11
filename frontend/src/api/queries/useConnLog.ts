import { keepPreviousData, useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { httpRequest } from '@/api/http-init';
import { keys } from '@/api/queryKeys';
import { ConnPageSchema } from '@/generated/zod';
import type { ConnFilter, ConnPage, ConnRecord } from '@/generated/types';

export type { ConnFilter, ConnPage, ConnRecord };

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchConnLog(filter: ConnFilter): Promise<ConnPage> {
  const msg = await HttpUtil.post('/panel/api/connlog/list', filter, {
    ...JSON_HEADERS,
    silent: true,
  });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the connection log');
  const validated = parseMsg(msg, ConnPageSchema, 'connlog/list', { strict: true });
  return validated.obj as ConnPage;
}

// Every query is written to the panel log on the server, so nothing refetches on its own.
export function useConnLogQuery(filter: ConnFilter) {
  return useQuery({
    queryKey: keys.connlog.list(filter),
    queryFn: () => fetchConnLog(filter),
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: false,
    staleTime: Infinity,
    gcTime: 0,
  });
}

// The export answers with CSV text; a refused filter answers with the usual JSON envelope.
export async function exportConnLog(filter: ConnFilter): Promise<string> {
  const resp = await httpRequest('POST', '/panel/api/connlog/export', filter, JSON_HEADERS);
  if (typeof resp.data !== 'string') {
    const msg = (resp.data as { msg?: string } | null)?.msg;
    throw new Error(msg || 'Export failed');
  }
  return resp.data;
}
