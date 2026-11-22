import { describe, expect, it } from 'vitest';

import type { ClientRecord, InboundOption } from '@/hooks/useClients';
import {
  amneziawgConfigFromLink,
  genAmneziaWGLinks,
  genInboundLinks,
} from '@/lib/xray/inbound-link';
import { buildAmneziaWGClientConfig } from '@/pages/clients/amneziawgConfig';
import type { Inbound } from '@/schemas/api/inbound';

// The browser cannot read geoip.dat, so every emitter must print the list the backend computed.
const LOCAL_DIRECT = '8.8.8.8/32, 91.108.0.0/16, 128.0.0.0/1, ::/0';

function allowedIPsLines(text: string): string[] {
  return [...text.matchAll(/^AllowedIPs = (.*)$/gm)].map((m) => m[1]);
}

const server = {
  publicKey: 'serverPubKey==',
  primaryDns: '8.8.8.8',
  jc: 4,
  jmin: 40,
  jmax: 100,
  s1: 30,
  s2: 90,
};

describe('AmneziaWG configs carry the backend local-direct AllowedIPs', () => {
  const inbound = {
    port: 51821,
    protocol: 'amneziawg',
    settings: {
      server,
      clients: [{ email: 'alice', privateKey: 'clientPrivKey==', allowedIPs: ['10.8.1.2/32'] }],
    },
    streamSettings: {},
  } as unknown as Inbound;
  const input = { inbound, remark: 'awg', fallbackHostname: 'panel.example.com' };

  it('inbounds page export: configs and vpn:// links use the list, and stay full tunnel without it', () => {
    expect(
      allowedIPsLines(genInboundLinks({ ...input, amneziaWGAllowedIPs: LOCAL_DIRECT })),
    ).toEqual([LOCAL_DIRECT]);
    const link = genAmneziaWGLinks({ ...input, amneziaWGAllowedIPs: LOCAL_DIRECT });
    expect(allowedIPsLines(amneziawgConfigFromLink(link))).toEqual([LOCAL_DIRECT]);
    expect(allowedIPsLines(genInboundLinks(input))).toEqual(['0.0.0.0/0, ::/0']);
  });

  it('clients page download/QR uses the inbound option list, and stays full tunnel without it', () => {
    const client = {
      email: 'alice',
      privateKey: 'clientPrivKey==',
      allowedIPs: '10.8.1.2/32',
    } as ClientRecord;
    const option = {
      id: 7,
      port: 51821,
      protocol: 'amneziawg',
      awgServer: server,
    } as unknown as InboundOption;
    const host = 'panel.example.com';
    expect(
      allowedIPsLines(
        buildAmneziaWGClientConfig(client, { ...option, awgAllowedIPs: LOCAL_DIRECT }, host),
      ),
    ).toEqual([LOCAL_DIRECT]);
    expect(allowedIPsLines(buildAmneziaWGClientConfig(client, option, host))).toEqual([
      '0.0.0.0/0, ::/0',
    ]);
  });
});
