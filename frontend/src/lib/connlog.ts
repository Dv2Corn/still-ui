import type { ConnFilter } from '@/generated/types';

export const CONNLOG_PAGE_ROWS = 50;
// Mirrors service.ConnLogExportMaxRows: the server ends an export there.
export const CONNLOG_EXPORT_MAX_ROWS = 100000;

export interface ConnLogFormValues {
  client: string;
  inbound: string;
  host: string;
  source: string;
  // Unix milliseconds; null leaves that side of the range open.
  from: number | null;
  to: number | null;
}

export const EMPTY_CONNLOG_FORM: ConnLogFormValues = {
  client: '',
  inbound: '',
  host: '',
  source: '',
  from: null,
  to: null,
};

// The API reads 0 as "no bound" and an empty string as "no filter".
export function connLogFilter(values: ConnLogFormValues, before = ''): ConnFilter {
  return {
    client: values.client.trim(),
    inbound: values.inbound.trim(),
    host: values.host.trim(),
    source: values.source.trim(),
    from: values.from ?? 0,
    to: values.to ?? 0,
    before,
    limit: CONNLOG_PAGE_ROWS,
  };
}

// An IPv6 address takes brackets before a port; a masked source and a stored-nothing value have no port.
export function connEndpoint(address: string, port: number): string {
  if (address === '') return '';
  if (port === 0) return address;
  return address.includes(':') ? `[${address}]:${port}` : `${address}:${port}`;
}
