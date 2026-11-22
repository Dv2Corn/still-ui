import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import AuditLogPage from '@/pages/audit/AuditLogPage';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

vi.mock('@/layouts/AppSidebar', () => ({ default: () => null }));

const entry = {
  id: 7,
  time: 1735732800000,
  actor: 'admin',
  ip: '203.0.113.9',
  action: 'POST /panel/api/inbounds/update/:id',
  target: 'id=3',
  outcome: 'failure',
  detail: 'HTTP 200',
  prevHash: 'a',
  hash: 'b',
};

function listCalls() {
  return vi
    .mocked(HttpUtil.post)
    .mock.calls.filter(([url]) => url === '/panel/api/audit/list')
    .map(([, body]) => body as { actor: string; outcome: string; page: number });
}

describe('Audit log page', () => {
  beforeEach(() => {
    vi.spyOn(HttpUtil, 'post').mockImplementation(async (url) => {
      if (url === '/panel/api/audit/verify') {
        return new Msg(true, '', {
          ok: false,
          checked: 6,
          brokenId: 7,
          reason: 'the entry was changed',
        });
      }
      return new Msg(true, '', { rows: [entry], total: 1 });
    });
  });

  it('sends the typed filter and shows which entry broke the chain', async () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/audit']}>
        <AuditLogPage />
      </MemoryRouter>,
    );
    await screen.findByText('POST /panel/api/inbounds/update/:id');
    expect(listCalls()[0]).toMatchObject({ actor: '', outcome: '', page: 1 });

    await userEvent.type(screen.getByRole('textbox', { name: 'User or token' }), 'admin');
    await userEvent.click(screen.getByRole('button', { name: /Search/ }));
    await waitFor(() => expect(listCalls()[1]).toMatchObject({ actor: 'admin', page: 1 }));

    await userEvent.click(screen.getByRole('button', { name: /Verify chain/ }));
    await screen.findByText('The chain is broken at entry #7: the entry was changed');
  });
});
