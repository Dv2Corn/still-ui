/// <reference types="vite/client" />
import { describe, expect, it } from 'vitest';

import { RealityStreamSettingsSchema } from '@/schemas/protocols/security/reality';
import {
  DEFAULT_REALITY_FINGERPRINT,
  createRealitySettingsForNewInbound,
} from '@/lib/xray/inbound-tls-defaults';

// still-ui: new REALITY inbounds start with the one-segment "ios" ClientHello.
describe('REALITY fingerprint default for new inbounds', () => {
  it('starts a new REALITY inbound with the ios fingerprint', () => {
    const reality = createRealitySettingsForNewInbound();
    expect(DEFAULT_REALITY_FINGERPRINT).toBe('ios');
    expect((reality.settings as Record<string, unknown>).fingerprint).toBe('ios');
  });

  it('does not rewrite the fingerprint an existing inbound stores', () => {
    const parsed = RealityStreamSettingsSchema.parse({
      settings: {
        publicKey: 'k',
        fingerprint: 'firefox',
        serverName: '',
        spiderX: '/',
        mldsa65Verify: '',
      },
    }) as { settings: { fingerprint: string } };
    expect(parsed.settings.fingerprint).toBe('firefox');
  });
});
