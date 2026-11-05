import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { ReachProbeView } from '@/api/queries/useReachProbes';
import type { ReachTargetView } from '@/generated/types';
import ProbeCard from '@/pages/probes/ProbeCard';
import { renderWithProviders } from './test-utils';

function target(key: string, patch: Partial<ReachTargetView> = {}): ReachTargetView {
  return {
    key,
    label: key,
    inboundId: Number(key.split('@')[0]),
    changedAt: 1_800_000_000_000,
    status: 'fail',
    consecutiveFails: 2,
    lastAt: 1_800_000_000_000,
    lastOk: false,
    lastTtfbMs: 0,
    lastTotalMs: 0,
    lastError: 'timeout',
    lastBytes: 0,
    checks7d: 10,
    uptime7d: 50,
    subState: '',
    subFailAgents: 0,
    subNeedAgents: 0,
    ...patch,
  };
}

function probe(targets: ReachTargetView[], subIgnored = ''): ReachProbeView {
  return {
    id: 1,
    name: 'home',
    enabled: true,
    clientEmail: 'still-probe-1',
    inboundIds: [1, 2, 3],
    testUrl: 'https://example.com/file',
    directUrl: '',
    intervalSec: 600,
    lastSeenAt: 1_800_000_000_000,
    agentVersion: '1.5.1',
    stale: false,
    createdAt: 1_700_000_000_000,
    targets,
    subIgnored,
  };
}

const noop = () => {};

describe('ProbeCard subscription state', () => {
  it('shows what subscriptions do with each endpoint', () => {
    renderWithProviders(
      <ProbeCard
        probe={probe([
          target('1@a.example.com:443', {
            subState: 'hidden',
            subFailAgents: 2,
            subNeedAgents: 2,
          }),
          target('2@b.example.com:443', {
            subState: 'demoted',
            subFailAgents: 1,
            subNeedAgents: 1,
          }),
          target('3@c.example.com:443', {
            subState: 'unconfirmed',
            subFailAgents: 1,
            subNeedAgents: 2,
          }),
        ])}
        onEdit={noop}
        onDelete={noop}
        onRotate={noop}
      />,
    );
    expect(screen.getByRole('columnheader', { name: 'Subscription' })).toBeTruthy();
    expect(screen.getByText('Hidden')).toBeTruthy();
    expect(screen.getByText('Moved down')).toBeTruthy();
    expect(screen.getByText('Unconfirmed 1/2')).toBeTruthy();
  });

  it('has no subscription column when subscriptions ignore the probes', () => {
    renderWithProviders(
      <ProbeCard
        probe={probe([target('1@a.example.com:443')])}
        onEdit={noop}
        onDelete={noop}
        onRotate={noop}
      />,
    );
    expect(screen.queryByRole('columnheader', { name: 'Subscription' })).toBeNull();
    expect(screen.queryByText('Not used by subscriptions')).toBeNull();
  });

  it('says when subscriptions leave a silent probe out', () => {
    renderWithProviders(
      <ProbeCard
        probe={probe([target('1@a.example.com:443')], 'silent')}
        onEdit={noop}
        onDelete={noop}
        onRotate={noop}
      />,
    );
    expect(screen.getByText('Not used by subscriptions')).toBeTruthy();
  });
});
