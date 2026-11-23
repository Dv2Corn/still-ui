import { RandomUtil } from '@/utils';
import { SniffingSchema } from '@/schemas/primitives/sniffing';
import { RealityStreamSettingsSchema } from '@/schemas/protocols/security/reality';
import { HysteriaStreamSettingsSchema } from '@/schemas/protocols/stream/hysteria';
import { TcpStreamSettingsSchema } from '@/schemas/protocols/stream/tcp';
import { XHttpStreamSettingsSchema } from '@/schemas/protocols/stream/xhttp';

import {
  createDefaultHysteriaInboundSettings,
  createDefaultVlessInboundSettings,
} from './inbound-defaults';
import { createHysteriaTlsSettingsWithDefaultCert } from './inbound-tls-defaults';

// Presets for clients on Russian mobile networks (DPI, whitelist mode).
export const INBOUND_PRESETS = ['ruMobileReality', 'ruMobileXhttp', 'ruMobileHysteria'] as const;
export type InboundPreset = (typeof INBOUND_PRESETS)[number];

// The ~517-byte ios ClientHello fits one TCP segment; larger ones lose the second segment
// on some mobile networks. Official Xray >= 26.9.9 rejects it, still-ui's patched build does not.
export const RU_MOBILE_FINGERPRINT = 'ios';

function baseInbound(): Record<string, unknown> {
  return {
    port: 443,
    listen: '',
    tag: '',
    remark: '',
    enable: true,
    trafficReset: 'never',
    sniffing: SniffingSchema.parse({}),
  };
}

function realitySettings(): Record<string, unknown> {
  const reality = RealityStreamSettingsSchema.parse({}) as Record<string, unknown>;
  const client = reality.settings as Record<string, unknown>;
  return {
    ...reality,
    target: '',
    serverNames: [],
    shortIds: RandomUtil.randomShortIds().split(','),
    settings: {
      ...client,
      fingerprint: RU_MOBILE_FINGERPRINT,
      spiderX: `/${RandomUtil.randomSeq(15)}`,
    },
  };
}

function ruMobileReality(): Record<string, unknown> {
  return {
    ...baseInbound(),
    protocol: 'vless',
    settings: createDefaultVlessInboundSettings(),
    streamSettings: {
      network: 'tcp',
      security: 'reality',
      tcpSettings: TcpStreamSettingsSchema.parse({ header: { type: 'none' } }),
      realitySettings: realitySettings(),
    },
  };
}

// Mode auto is stream-one under REALITY in Xray 26.10.31; Vision needs raw TCP, so the
// clients get no flow. A random path keeps probes for the default "/" from finding it.
function ruMobileXhttp(): Record<string, unknown> {
  return {
    ...baseInbound(),
    protocol: 'vless',
    settings: createDefaultVlessInboundSettings(),
    streamSettings: {
      network: 'xhttp',
      security: 'reality',
      xhttpSettings: XHttpStreamSettingsSchema.parse({
        path: `/${RandomUtil.randomLowerAndNum(12)}`,
      }),
      realitySettings: realitySettings(),
    },
  };
}

// No Salamander: plain QUIC is what most mobile clients can open; add it on Stream if needed.
function ruMobileHysteria(): Record<string, unknown> {
  return {
    ...baseInbound(),
    protocol: 'hysteria',
    settings: createDefaultHysteriaInboundSettings(),
    streamSettings: {
      network: 'hysteria',
      security: 'tls',
      hysteriaSettings: HysteriaStreamSettingsSchema.parse({}),
      tlsSettings: createHysteriaTlsSettingsWithDefaultCert(),
      finalmask: { tcp: [], udp: [] },
    },
  };
}

const PRESET_BUILDERS: Record<InboundPreset, () => Record<string, unknown>> = {
  ruMobileReality,
  ruMobileXhttp,
  ruMobileHysteria,
};

/** Raw inbound (wire shape) for the add form; the REALITY keypair comes from the server. */
export function buildPresetInbound(preset: InboundPreset): Record<string, unknown> {
  return PRESET_BUILDERS[preset]();
}
