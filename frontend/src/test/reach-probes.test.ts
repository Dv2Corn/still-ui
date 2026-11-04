import { describe, expect, it } from 'vitest';

import { probeHistoryBuckets, probeInstallCommand } from '@/lib/probes';

describe('probeInstallCommand', () => {
  it('quotes the panel URL and token so a shell cannot split or expand them', () => {
    const cmd = probeInstallCommand("https://panel.example.com:2053/a'b$(id)/", 'tok$en');
    expect(cmd).toContain(`--panel 'https://panel.example.com:2053/a'\\''b$(id)/'`);
    expect(cmd).toContain(`STILL_PROBE_TOKEN='tok$en' bash `);
  });

  it('keeps the token off the installer command line', () => {
    const cmd = probeInstallCommand('https://panel.example.com/', 'secrettoken');
    expect(cmd).not.toContain('--token');
    expect(cmd.startsWith(`STILL_PROBE_TOKEN='secrettoken' bash <(curl -fsSL `)).toBe(true);
  });
});

describe('probeHistoryBuckets', () => {
  const hour = 3_600_000;
  const now = 1_800_000_000_000;

  it('puts each result of one target into its hour and grades the hour', () => {
    const points = [
      { key: 'a', at: now - 3 * hour + 1, ok: true },
      { key: 'a', at: now - 2 * hour + 5, ok: false },
      { key: 'a', at: now - hour + 1, ok: true },
      { key: 'a', at: now - hour + 2, ok: false },
      { key: 'b', at: now - hour + 3, ok: false },
      { key: 'a', at: now - 200 * hour, ok: false },
    ];
    const buckets = probeHistoryBuckets(points, 'a', now, 4);
    expect(buckets).toEqual(['none', 'ok', 'fail', 'mixed']);
  });
});
