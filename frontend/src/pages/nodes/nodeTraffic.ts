import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { NodeTrafficLimitPayload } from '@/api/queries/useNodeMutations';

const GB = 1024 * 1024 * 1024;

// The form edits the limit in GB; the API stores bytes. Rounded to whole bytes so a
// fractional GB never posts a float the backend's int64 field would reject.
export function gbToBytes(gb: number | null | undefined): number {
  if (gb == null || !Number.isFinite(gb) || gb <= 0) return 0;
  return Math.round(gb * GB);
}

export function bytesToGb(bytes: number | null | undefined): number {
  if (bytes == null || !Number.isFinite(bytes) || bytes <= 0) return 0;
  return Math.round((bytes / GB) * 100) / 100;
}

// Percent of the limit used, capped at 100 for the progress bar; 0 without a limit.
export function trafficPercent(
  node: Pick<NodeRecord, 'trafficLimitBytes' | 'trafficUsed'>,
): number {
  const limit = node.trafficLimitBytes ?? 0;
  if (limit <= 0) return 0;
  return Math.min(100, Math.floor(((node.trafficUsed ?? 0) * 100) / limit));
}

// The stored quota of a node as the form's starting values; nodes saved before the
// quota existed fall back to the server defaults.
export function trafficFormValues(node: NodeRecord | null): NodeTrafficLimitPayload {
  return {
    trafficLimitBytes: node?.trafficLimitBytes ?? 0,
    trafficResetDay: node?.trafficResetDay || 1,
    trafficWarnPercent: node?.trafficWarnPercent ?? 80,
    trafficLimitAction: node?.trafficLimitAction === 'disable' ? 'disable' : 'notify',
    trafficMultiplier: node?.trafficMultiplier || 1,
  };
}
