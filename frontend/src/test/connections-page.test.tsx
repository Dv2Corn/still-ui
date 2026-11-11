import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import ConnectionsPage from '@/pages/connections/ConnectionsPage';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

vi.mock('@/layouts/AppSidebar', () => ({ default: () => null }));

function record(host: string) {
  return {
    time: 1735732800000,
    client: 'alice@example.com',
    inbound: 'inbound-443',
    outbound: 'direct',
    source: '203.0.113.9',
    sourcePort: 54321,
    network: 'tcp',
    host,
    port: 443,
  };
}

function listCalls() {
  return vi
    .mocked(HttpUtil.post)
    .mock.calls.filter(([url]) => url === '/panel/api/connlog/list')
    .map(([, body]) => body as { client: string; before: string });
}

describe('Connections page', () => {
  beforeEach(() => {
    vi.spyOn(HttpUtil, 'get').mockImplementation(async () => new Msg(true, '', []));
    vi.spyOn(HttpUtil, 'post').mockImplementation(async (url, body) => {
      if (url !== '/panel/api/connlog/list') return new Msg(true, '', {});
      const first = (body as { before: string }).before === '';
      return new Msg(true, '', {
        rows: [record(first ? 'first.example.com' : 'second.example.com')],
        next: first ? '20250101:7' : '',
        enabled: true,
        sizeBytes: 4096,
      });
    });
  });

  it('opens on the client a row of the Clients page linked to and pages with the cursor', async () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/connections?client=alice%40example.com']}>
        <ConnectionsPage />
      </MemoryRouter>,
    );
    await screen.findByText('first.example.com:443');
    expect(listCalls()).toEqual([
      expect.objectContaining({ client: 'alice@example.com', before: '' }),
    ]);

    await userEvent.click(screen.getByRole('button', { name: /Older/ }));
    await screen.findByText('second.example.com:443');
    expect(listCalls()[1]).toMatchObject({ client: 'alice@example.com', before: '20250101:7' });
    expect(screen.getByRole<HTMLButtonElement>('button', { name: /Older/ }).disabled).toBe(true);

    await userEvent.click(screen.getByRole('button', { name: /Newer/ }));
    await screen.findByText('first.example.com:443');
    await waitFor(() => expect(listCalls()[2]).toMatchObject({ before: '' }));
  });

  it('asks the server again when Search is pressed with the same filter', async () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/connections']}>
        <ConnectionsPage />
      </MemoryRouter>,
    );
    await screen.findByText('first.example.com:443');
    expect(listCalls()).toHaveLength(1);
    await userEvent.click(screen.getByRole('button', { name: /Search/ }));
    await waitFor(() => expect(listCalls()).toHaveLength(2));
    expect(listCalls()[1]).toMatchObject({ client: '', before: '' });
  });

  it('says that nothing is being recorded while the log is off', async () => {
    vi.mocked(HttpUtil.post).mockImplementation(
      async (url) =>
        new Msg(
          true,
          '',
          url === '/panel/api/connlog/list'
            ? { rows: [], next: '', enabled: false, sizeBytes: 0 }
            : {},
        ),
    );
    renderWithProviders(
      <MemoryRouter initialEntries={['/connections']}>
        <ConnectionsPage />
      </MemoryRouter>,
    );
    await screen.findByText('The connection log is off. Nothing is being recorded.');
    expect(listCalls()[0]).toMatchObject({ client: '' });
  });
});
