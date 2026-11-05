import { describe, expect, it } from 'vitest';

import { bytesToGb, gbToBytes, trafficFormValues, trafficPercent } from '@/pages/nodes/nodeTraffic';

const GB = 1024 ** 3;

describe('node traffic helpers', () => {
  it('posts the limit as whole bytes, 0 for an empty or negative field', () => {
    expect(gbToBytes(1.5)).toBe(1.5 * GB);
    expect(Number.isInteger(gbToBytes(0.333))).toBe(true);
    expect(gbToBytes(null)).toBe(0);
    expect(gbToBytes(-3)).toBe(0);
    expect(bytesToGb(1024 * GB)).toBe(1024);
    expect(bytesToGb(0)).toBe(0);
  });

  it('caps the bar at 100 % and shows 0 without a limit', () => {
    expect(trafficPercent({ trafficLimitBytes: 100, trafficUsed: 79 })).toBe(79);
    expect(trafficPercent({ trafficLimitBytes: 100, trafficUsed: 250 })).toBe(100);
    expect(trafficPercent({ trafficLimitBytes: 0, trafficUsed: 250 })).toBe(0);
  });

  it('starts a node saved before the quota existed from the server defaults', () => {
    expect(trafficFormValues({ id: 1 })).toEqual({
      trafficLimitBytes: 0,
      trafficResetDay: 1,
      trafficWarnPercent: 80,
      trafficLimitAction: 'notify',
      trafficMultiplier: 1,
    });
    expect(
      trafficFormValues({ id: 1, trafficLimitAction: 'disable', trafficWarnPercent: 0 }),
    ).toMatchObject({ trafficLimitAction: 'disable', trafficWarnPercent: 0 });
  });
});
