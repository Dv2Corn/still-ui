import { describe, expect, it } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import { applyExitAddressPreset, exitAddressError } from '@/lib/xray/exit-address-preset';

const apiRule = { type: 'field', inboundTag: ['api'], outboundTag: 'api' };

function template(): XraySettingsValue {
  return {
    outbounds: [{ tag: 'direct', protocol: 'freedom', settings: {} }],
    routing: { rules: [apiRule] },
  } as XraySettingsValue;
}

describe('applyExitAddressPreset', () => {
  it('adds a freedom outbound bound to the exit IP and appends a rule for the chosen inbounds', () => {
    const tt = template();
    applyExitAddressPreset(tt, '203.0.113.7', ['in-443', 'in-8443']);

    expect(tt.outbounds).toEqual([
      { tag: 'direct', protocol: 'freedom', settings: {} },
      {
        tag: 'exit-203.0.113.7',
        protocol: 'freedom',
        sendThrough: '203.0.113.7',
        settings: {
          finalRules: [{ action: 'block', ip: ['geoip:private'] }, { action: 'allow' }],
        },
      },
    ]);
    // Appended, so earlier block/api rules still win over the new catch-all for these inbounds.
    expect(tt.routing?.rules).toEqual([
      apiRule,
      { type: 'field', inboundTag: ['in-443', 'in-8443'], outboundTag: 'exit-203.0.113.7' },
    ]);
  });

  it('creates the outbounds and rules arrays on an empty template', () => {
    const tt = {} as XraySettingsValue;
    applyExitAddressPreset(tt, '198.51.100.2', ['in-1']);
    expect(tt.outbounds?.map((o) => o?.tag)).toEqual(['exit-198.51.100.2']);
    expect(tt.routing?.rules).toEqual([
      { type: 'field', inboundTag: ['in-1'], outboundTag: 'exit-198.51.100.2' },
    ]);
  });
});

describe('exitAddressError', () => {
  const inbound = { tag: 'in-443', listen: '203.0.113.5' };

  it('rejects anything that is not a dotted IPv4 address', () => {
    for (const ip of ['', '203.0.113', '203.0.113.256', '2001:db8::1', 'example.com']) {
      expect(exitAddressError(ip, [inbound], [])).toBe('pages.xray.exitAddress.invalidIp');
    }
  });

  it('rejects the address a selected inbound listens on', () => {
    expect(exitAddressError('203.0.113.5', [inbound], [])).toBe(
      'pages.xray.exitAddress.sameAsInbound',
    );
  });

  it('rejects the custom share address a selected inbound advertises', () => {
    const advertised = {
      tag: 'in-8443',
      listen: '',
      shareAddr: '203.0.113.9',
      shareAddrStrategy: 'custom',
    };
    expect(exitAddressError('203.0.113.9', [advertised], [])).toBe(
      'pages.xray.exitAddress.sameAsInbound',
    );
    // A stored shareAddr is ignored unless the inbound actually advertises it.
    expect(
      exitAddressError('203.0.113.9', [{ ...advertised, shareAddrStrategy: 'listen' }], []),
    ).toBe(null);
  });

  it('rejects an IP whose outbound tag already exists', () => {
    expect(exitAddressError('203.0.113.7', [inbound], ['direct', 'exit-203.0.113.7'])).toBe(
      'pages.xray.exitAddress.tagTaken',
    );
  });

  it('accepts a different IPv4 address', () => {
    expect(exitAddressError('203.0.113.7', [inbound], ['direct'])).toBe(null);
  });
});
