import { z } from 'zod';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import type { InboundOption } from '@/schemas/client';

export type ExitInbound = Pick<InboundOption, 'tag' | 'listen' | 'shareAddr' | 'shareAddrStrategy'>;

const ipv4 = z.ipv4();

export function exitOutboundTag(ip: string): string {
  return `exit-${ip}`;
}

// Returns an i18n key, or null when the IP can be used as the exit address.
export function exitAddressError(
  ip: string,
  selected: ExitInbound[],
  existingOutboundTags: string[],
): string | null {
  if (!ipv4.safeParse(ip).success) return 'pages.xray.exitAddress.invalidIp';
  const entryAddresses = selected.flatMap((ib) => [
    ib.listen,
    ib.shareAddrStrategy === 'custom' ? ib.shareAddr : undefined,
  ]);
  if (entryAddresses.includes(ip)) return 'pages.xray.exitAddress.sameAsInbound';
  if (existingOutboundTags.includes(exitOutboundTag(ip))) return 'pages.xray.exitAddress.tagTaken';
  return null;
}

// The rule is appended so earlier api/block rules still apply to these inbounds.
export function applyExitAddressPreset(
  tt: XraySettingsValue,
  ip: string,
  inboundTags: string[],
): void {
  const tag = exitOutboundTag(ip);
  if (!Array.isArray(tt.outbounds)) tt.outbounds = [];
  tt.outbounds.push({
    tag,
    protocol: 'freedom',
    sendThrough: ip,
    // Same private-network block as the stock `direct` outbound.
    settings: { finalRules: [{ action: 'block', ip: ['geoip:private'] }, { action: 'allow' }] },
  });
  if (!tt.routing) tt.routing = {};
  if (!Array.isArray(tt.routing.rules)) tt.routing.rules = [];
  tt.routing.rules.push({ type: 'field', inboundTag: [...inboundTags], outboundTag: tag });
}
