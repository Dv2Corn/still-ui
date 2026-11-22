import { keepPreviousData, useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { httpRequest } from '@/api/http-init';
import { keys } from '@/api/queryKeys';
import { AuditPageSchema, AuditVerifyResultSchema } from '@/generated/zod';
import type { AuditFilter, AuditLog, AuditPage, AuditVerifyResult } from '@/generated/types';

export type { AuditFilter, AuditLog, AuditPage, AuditVerifyResult };

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

async function fetchAuditLog(filter: AuditFilter): Promise<AuditPage> {
  const msg = await HttpUtil.post('/panel/api/audit/list', filter, {
    ...JSON_HEADERS,
    silent: true,
  });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the audit log');
  const validated = parseMsg(msg, AuditPageSchema, 'audit/list', { strict: true });
  return validated.obj as AuditPage;
}

// Every query is itself journaled on the server, so nothing refetches on its own.
export function useAuditLogQuery(filter: AuditFilter) {
  return useQuery({
    queryKey: keys.audit.list(filter),
    queryFn: () => fetchAuditLog(filter),
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: false,
    staleTime: Infinity,
    gcTime: 0,
  });
}

// The export answers with CSV text; a refused filter answers with the usual JSON envelope.
export async function exportAuditLog(filter: AuditFilter): Promise<string> {
  const resp = await httpRequest('POST', '/panel/api/audit/export', filter, JSON_HEADERS);
  if (typeof resp.data !== 'string') {
    const msg = (resp.data as { msg?: string } | null)?.msg;
    throw new Error(msg || 'Export failed');
  }
  return resp.data;
}

export async function verifyAuditLog(): Promise<AuditVerifyResult> {
  const msg = await HttpUtil.post('/panel/api/audit/verify', {}, { ...JSON_HEADERS, silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Verify failed');
  const validated = parseMsg(msg, AuditVerifyResultSchema, 'audit/verify', { strict: true });
  return validated.obj as AuditVerifyResult;
}
