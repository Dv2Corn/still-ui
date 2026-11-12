import { describe, expect, it } from 'vitest';

import { buildPresetInbound } from '@/lib/xray/inbound-presets';
import { formValuesToWirePayload, rawInboundToFormValues } from '@/lib/xray/inbound-form-adapter';
import { inboundFromDb } from '@/lib/xray/inbound-from-db';
import { genHysteriaLink, genVlessLink } from '@/lib/xray/inbound-link';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';

interface PresetStream {
  network: string;
  xhttpSettings: { path: string; mode: string; xPaddingBytes: string };
  realitySettings: {
    target: string;
    serverNames: string[];
    privateKey: string;
    shortIds: string[];
    settings: { publicKey: string };
  };
  tlsSettings: {
    certificates: Array<{ certificateFile: string; keyFile: string }>;
  };
}

// The preset goes through the same parse + wire step as the modal's Save.
function savedPreset(
  preset: Parameters<typeof buildPresetInbound>[0],
  edit: (stream: PresetStream) => void,
) {
  const raw = buildPresetInbound(preset);
  edit(raw.streamSettings as PresetStream);
  const parsed = InboundFormSchema.safeParse(rawInboundToFormValues(raw));
  if (!parsed.success) throw new Error(JSON.stringify(parsed.error.issues));
  const wire = formValuesToWirePayload(parsed.data);
  return { wire, stream: JSON.parse(wire.streamSettings) as PresetStream };
}

describe('mobile-network presets', () => {
  it('VLESS REALITY preset puts fp=ios into the client link of that inbound', () => {
    const { wire, stream } = savedPreset('ruMobileReality', ({ realitySettings: reality }) => {
      reality.target = 'www.example.com:443';
      reality.serverNames = ['www.example.com'];
      reality.privateKey = 'kOm2OyDh8mxa0k3WjUkLvgFzZh6dlx3ou5tycbS2c2I';
      reality.settings.publicKey = 'Zx1OSMYqtjtOAQ9dgK8-WCm76_2iLMiH0O2uqfUu2DU';
    });
    expect(wire).toMatchObject({ protocol: 'vless', port: 443 });
    expect(stream).toMatchObject({ network: 'tcp', security: 'reality' });
    const shortIds = stream.realitySettings.shortIds;
    expect(shortIds.length).toBeGreaterThan(0);
    for (const id of shortIds) expect(id).toMatch(/^(?:[0-9a-f]{2}){1,8}$/);

    const inbound = inboundFromDb({
      ...wire,
      settings: {
        ...JSON.parse(wire.settings),
        clients: [
          {
            id: '8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a',
            email: 'a',
            flow: 'xtls-rprx-vision',
          },
        ],
      },
      streamSettings: stream,
      sniffing: JSON.parse(wire.sniffing),
    });
    const link = new URL(
      genVlessLink({
        inbound,
        address: 'vpn.example.test',
        clientId: '8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a',
        flow: 'xtls-rprx-vision',
      }),
    );
    expect(link.searchParams.get('fp')).toBe('ios');
    expect(link.searchParams.get('security')).toBe('reality');
    expect(link.searchParams.get('flow')).toBe('xtls-rprx-vision');
  });

  // Vision needs raw TCP: a vision client on this inbound would be refused by the server,
  // so its links must carry no flow; a fixed path would let anyone probe for the inbound.
  it('VLESS XHTTP REALITY preset links carry the transport and no Vision flow', () => {
    const { wire, stream } = savedPreset('ruMobileXhttp', ({ realitySettings: reality }) => {
      reality.target = 'www.example.com:443';
      reality.serverNames = ['www.example.com'];
      reality.privateKey = 'kOm2OyDh8mxa0k3WjUkLvgFzZh6dlx3ou5tycbS2c2I';
      reality.settings.publicKey = 'Zx1OSMYqtjtOAQ9dgK8-WCm76_2iLMiH0O2uqfUu2DU';
    });
    expect(wire).toMatchObject({ protocol: 'vless', port: 443 });
    expect(stream).toMatchObject({
      network: 'xhttp',
      security: 'reality',
      xhttpSettings: { mode: 'auto', xPaddingBytes: '100-1000' },
    });
    const path = stream.xhttpSettings.path;
    expect(path).not.toBe('/');
    const again = buildPresetInbound('ruMobileXhttp').streamSettings as PresetStream;
    expect(again.xhttpSettings.path).not.toBe(path);

    const inbound = inboundFromDb({
      ...wire,
      settings: {
        ...JSON.parse(wire.settings),
        clients: [{ id: '8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a', email: 'a', flow: '' }],
      },
      streamSettings: stream,
      sniffing: JSON.parse(wire.sniffing),
    });
    const link = new URL(
      genVlessLink({
        inbound,
        address: 'vpn.example.test',
        clientId: '8b6f1a1e-6d39-4b0c-9a5e-3c1f0d1c2b3a',
        flow: 'xtls-rprx-vision',
      }),
    );
    expect(Object.fromEntries(link.searchParams)).toMatchObject({
      type: 'xhttp',
      security: 'reality',
      fp: 'ios',
      sni: 'www.example.com',
      path,
      mode: 'auto',
    });
    expect(link.searchParams.get('flow')).toBeNull();
  });

  it('Hysteria2 preset listens on UDP 443 without Salamander in the link', () => {
    const { wire, stream } = savedPreset('ruMobileHysteria', ({ tlsSettings }) => {
      const cert = tlsSettings.certificates[0];
      cert.certificateFile = '/etc/ssl/certs/h.crt';
      cert.keyFile = '/etc/ssl/private/h.key';
    });
    expect(wire).toMatchObject({ protocol: 'hysteria', port: 443 });
    expect(stream).toMatchObject({
      network: 'hysteria',
      security: 'tls',
      tlsSettings: { alpn: ['h3'] },
    });

    const inbound = inboundFromDb({
      ...wire,
      settings: {
        ...JSON.parse(wire.settings),
        clients: [{ auth: 'secret-auth', email: 'a' }],
      },
      streamSettings: stream,
      sniffing: JSON.parse(wire.sniffing),
    });
    const link = genHysteriaLink({
      inbound,
      address: 'vpn.example.test',
      clientAuth: 'secret-auth',
    });
    expect(link.startsWith('hysteria2://')).toBe(true);
    expect(new URL(link.replace(/^hysteria2:/, 'http:')).searchParams.get('obfs')).toBeNull();
    expect(new URL(link.replace(/^hysteria2:/, 'http:')).port).toBe('443');
  });
});
