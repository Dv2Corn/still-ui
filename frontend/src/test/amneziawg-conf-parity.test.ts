import { describe, it, expect } from 'vitest';

import { genAmneziaWGConfig } from '@/lib/xray/inbound-link';
import { buildAmneziaWGClientConfig } from '@/pages/clients/amneziawgConfig';
import type { AmneziawgInboundSettings } from '@/schemas/protocols/inbound/amneziawg';
import type { ClientRecord, InboundOption } from '@/hooks/useClients';

// wg-quick(8)'s own peer order. The panel emits an AmneziaWG .conf from three
// independent places (this file's two, plus amneziaWGConfigText in Go), and a
// user comparing a subscription link against a downloaded .conf sees any drift
// between them immediately.
const PEER_FIELD_ORDER = [
  'PublicKey',
  'PresharedKey',
  'AllowedIPs',
  'Endpoint',
  'PersistentKeepalive',
];

function peerFields(conf: string): string[] {
  const peerBlock = conf.slice(conf.indexOf('[Peer]'));
  return peerBlock
    .split('\n')
    .map((line) => line.split('=')[0].trim())
    .filter((key) => PEER_FIELD_ORDER.includes(key));
}

describe('AmneziaWG .conf emitters agree on the peer block', () => {
  const settings = {
    server: {
      publicKey: 'serverPubKey==',
      primaryDns: '8.8.8.8',
      secondaryDns: '',
      mtu: 1420,
      jc: 4,
      jmin: 40,
      jmax: 100,
      s1: 30,
      s2: 90,
      s3: 0,
      s4: 0,
      h1: '',
      h2: '',
      h3: '',
      h4: '',
    },
    clients: [
      {
        email: 'peer-1',
        privateKey: 'clientPrivKey==',
        allowedIPs: ['10.8.1.2/32'],
        preSharedKey: 'psk==',
        keepAlive: 25,
      },
    ],
  } as unknown as AmneziawgInboundSettings;

  const linkConf = genAmneziaWGConfig({
    settings,
    address: 'awg.example.test',
    port: 51820,
    remark: 'awg-peer-1',
    peerIndex: 0,
  });

  const client = {
    email: 'peer-1',
    privateKey: 'clientPrivKey==',
    allowedIPs: '10.8.1.2/32',
    preSharedKey: 'psk==',
    keepAlive: 25,
  } as unknown as ClientRecord;
  const inbound = {
    id: 1,
    tag: 'awg-1',
    remark: 'awg',
    port: 51820,
    protocol: 'amneziawg',
    awgServer: settings.server,
  } as unknown as InboundOption;
  const clientsPageConf = buildAmneziaWGClientConfig(client, inbound, 'awg.example.test');

  it('the share-link emitter uses the wg-quick peer order', () => {
    expect(peerFields(linkConf)).toEqual(PEER_FIELD_ORDER);
  });

  it('the clients-page emitter uses the same order', () => {
    expect(peerFields(clientsPageConf)).toEqual(PEER_FIELD_ORDER);
  });

  it('neither emitter leaves a trailing newline, so both end on their last set field', () => {
    expect(linkConf.endsWith('\n')).toBe(false);
    expect(clientsPageConf.endsWith('\n')).toBe(false);
  });

  it('an unset preSharedKey drops the line in both, without disturbing the rest', () => {
    const noPsk = {
      ...settings,
      clients: [{ ...settings.clients[0], preSharedKey: '' }],
    } as AmneziawgInboundSettings;
    const withoutPsk = genAmneziaWGConfig({
      settings: noPsk,
      address: 'awg.example.test',
      port: 51820,
      remark: 'awg-peer-1',
      peerIndex: 0,
    });
    const clientWithoutPsk = { ...client, preSharedKey: '' } as unknown as ClientRecord;
    const want = PEER_FIELD_ORDER.filter((f) => f !== 'PresharedKey');
    expect(peerFields(withoutPsk)).toEqual(want);
    expect(
      peerFields(buildAmneziaWGClientConfig(clientWithoutPsk, inbound, 'awg.example.test')),
    ).toEqual(want);
  });
});

// s4 junk is prepended to every transport packet and never clamped to the MTU,
// so both emitters must write the same S4-aware value the server interface uses.
describe('AmneziaWG .conf emitters agree on MTU', () => {
  function build(mtu: number | undefined, s4: number) {
    const settings = {
      server: {
        publicKey: 'serverPubKey==',
        primaryDns: '8.8.8.8',
        secondaryDns: '',
        mtu,
        jc: 4,
        jmin: 40,
        jmax: 100,
        s1: 30,
        s2: 90,
        s3: 0,
        s4,
        h1: '',
        h2: '',
        h3: '',
        h4: '',
      },
      clients: [{ email: 'peer-1', privateKey: 'clientPrivKey==', allowedIPs: ['10.8.1.2/32'] }],
    } as unknown as AmneziawgInboundSettings;

    const link = genAmneziaWGConfig({
      settings,
      address: 'awg.example.test',
      port: 51820,
      remark: 'awg-peer-1',
      peerIndex: 0,
    });
    const download = buildAmneziaWGClientConfig(
      {
        email: 'peer-1',
        privateKey: 'clientPrivKey==',
        allowedIPs: '10.8.1.2/32',
      } as unknown as ClientRecord,
      {
        id: 1,
        tag: 'awg-1',
        remark: 'awg',
        protocol: 'amneziawg',
        port: 51820,
        awgServer: settings.server,
      } as unknown as InboundOption,
      'awg.example.test',
    );
    return { link, download };
  }

  function mtuLine(conf: string): string | undefined {
    return conf.split('\n').find((l) => l.startsWith('MTU = '));
  }

  it('always emits an MTU, 1280 when the inbound has none set', () => {
    const { link, download } = build(undefined, 27);
    // Without this line the client stays on its own ~1420 default, several
    // times slower on paths below a 1500-byte MTU.
    expect(mtuLine(link)).toBe('MTU = 1280');
    expect(mtuLine(download)).toBe('MTU = 1280');
  });

  it('keeps an explicit MTU untouched', () => {
    const { link, download } = build(1380, 27);
    expect(mtuLine(link)).toBe('MTU = 1380');
    expect(mtuLine(download)).toBe('MTU = 1380');
  });

  it('gives 1280 regardless of s4 when unset', () => {
    const { link, download } = build(undefined, 0);
    expect(mtuLine(link)).toBe('MTU = 1280');
    expect(mtuLine(download)).toBe('MTU = 1280');
  });
});
