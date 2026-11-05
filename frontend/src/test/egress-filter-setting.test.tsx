import { describe, expect, it, onTestFinished, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import EgressFilterSetting from '@/pages/xray/routing/EgressFilterSetting';
import {
  egressHostNotice,
  splitCustomEntries,
  type EgressFilterStatus,
} from '@/api/queries/useEgressFilterQuery';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

function status(patch: Partial<EgressFilterStatus> = {}): EgressFilterStatus {
  return {
    settings: {
      smtp: true,
      mailSubmission: false,
      private: false,
      server: false,
      custom: null,
      nodeScope: '',
      nodeIds: null,
    },
    host: {
      supported: true,
      nftInstalled: true,
      active: true,
      counters: { mail: 0, private: 0, server: 0, custom: 0 },
    },
    hostNeeded: true,
    serverPorts: [22, 2053],
    templatePrivateBlocked: true,
    nodes: [],
    ...patch,
  };
}

function serve(st: EgressFilterStatus, nodes: unknown[] = []) {
  const get = vi.mocked(HttpUtil.get);
  const fallback = get.getMockImplementation()!;
  get.mockImplementation(async (url, ...rest) => {
    if (url === '/panel/api/server/egressFilter') return new Msg(true, '', st);
    if (url === '/panel/api/nodes/list') return new Msg(true, '', nodes);
    return fallback(url, ...rest);
  });
  onTestFinished(() => {
    get.mockImplementation(fallback);
  });
}

describe('EgressFilterSetting', () => {
  it('sends the ticked blocks, custom entries and node scope as JSON', async () => {
    serve(status(), [{ id: 4, name: 'de-1', enable: true, status: 'online' }]);
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    post.mockResolvedValueOnce(new Msg(true, '', status()));
    renderWithProviders(<EgressFilterSetting />);

    const smtp = await screen.findByRole('checkbox', { name: 'Mail to mail servers (port 25)' });
    await waitFor(() => expect(smtp.hasAttribute('disabled')).toBe(false));
    expect((smtp as HTMLInputElement).checked).toBe(true);
    const apply = screen.getByRole('button', { name: 'Apply' });
    expect(apply.hasAttribute('disabled')).toBe(true);

    fireEvent.click(screen.getByRole('checkbox', { name: /^Private and local networks/ }));
    await screen.findByText(/already blocks these with the template's geoip:private rule/);
    fireEvent.click(screen.getByRole('checkbox', { name: "This server's panel and SSH ports" }));
    await screen.findByText(/Ports now: 22, 2053\. The subscription port stays open/);

    const custom = screen.getByRole('combobox', { name: 'Custom addresses and ports' });
    fireEvent.change(custom, { target: { value: '203.0.113.0/24, 6667' } });
    fireEvent.keyDown(custom, { key: 'Enter', code: 'Enter' });

    fireEvent.click(screen.getByRole('radio', { name: 'All nodes' }));
    await waitFor(() => expect(apply.hasAttribute('disabled')).toBe(false));
    fireEvent.click(apply);

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/panel/api/server/setEgressFilter',
        {
          smtp: true,
          mailSubmission: false,
          private: true,
          server: true,
          custom: ['203.0.113.0/24', '6667'],
          nodeScope: 'all',
          nodeIds: [],
        },
        { headers: { 'Content-Type': 'application/json' } },
      ),
    );
  });

  it('explains a managed filter, a missing nft and where nodes are', async () => {
    serve(
      status({
        settings: { ...status().settings, managedBy: 'central-guid', nodeScope: 'all' },
        host: {
          supported: true,
          nftInstalled: false,
          active: false,
          counters: status().host.counters,
        },
        error: 'nft is not installed',
        nodes: [
          { id: 1, name: 'de-1', wanted: 'apply', synced: true },
          { id: 2, name: 'fi-1', wanted: 'apply', synced: false, error: 'connection refused' },
          { id: 3, name: 'nl-1', wanted: 'release', synced: false },
        ],
      }),
    );
    renderWithProviders(<EgressFilterSetting />);

    await screen.findByText(/Set by the central panel/);
    await screen.findByText(/nft is not installed: only traffic through Xray is filtered/);
    await screen.findByText('de-1: filter applied');
    await screen.findByText('fi-1: connection refused');
    await screen.findByText('nl-1: waiting for the node');
  });

  it('counts what the loaded table refused', async () => {
    serve(
      status({
        host: {
          supported: true,
          nftInstalled: true,
          active: true,
          counters: { mail: 12, private: 3, server: 1, custom: 0 },
        },
      }),
    );
    renderWithProviders(<EgressFilterSetting />);
    await screen.findByText(/mail 12, private networks 3, server ports 1, custom 0 packets/);
    expect(screen.queryByRole('alert')).toBeNull();
  });
});

describe('egress filter helpers', () => {
  it('splits pasted lists and keeps quiet when the host half is not needed', () => {
    expect(splitCustomEntries(['10.0.0.0/8, 25;465', ' 6667 ', ''])).toEqual([
      '10.0.0.0/8',
      '25',
      '465',
      '6667',
    ]);
    const off = status({
      hostNeeded: false,
      host: { ...status().host, supported: false, active: false },
    });
    expect(egressHostNotice(off)).toBe('');
    expect(egressHostNotice(status({ host: { ...status().host, supported: false } }))).toBe(
      'unsupported',
    );
    expect(egressHostNotice(status({ host: { ...status().host, active: false } }))).toBe('error');
    expect(egressHostNotice(status())).toBe('');
  });
});
