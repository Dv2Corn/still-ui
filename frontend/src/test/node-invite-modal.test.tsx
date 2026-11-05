import type { ReactNode } from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import NodeInviteModal from '@/pages/nodes/NodeInviteModal';
import { nodeEnrollCommand } from '@/lib/node-enroll';
import { HttpUtil } from '@/utils';

import { makeTestQueryClient } from './test-utils';

const SECRET = 'still-node1.secretsecret.pin.url';

function invite(id: number, status: string) {
  return {
    id,
    name: `n${id}`,
    address: '',
    allowPrivateAddress: false,
    nodeAdmin: false,
    masterUrl: 'https://m.example.com/',
    status,
    createdAt: 1_800_000_000,
    expiresAt: 1_800_001_800,
    usedAt: status === 'used' ? 1_800_000_600 : 0,
    usedFromIp: status === 'used' ? '203.0.113.7' : '',
    nodeId: status === 'used' ? 4 : 0,
    revokedAt: 0,
    attempts: 0,
    lastError: '',
  };
}

function renderModal() {
  const queryClient = makeTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return render(<NodeInviteModal open onClose={() => {}} />, { wrapper });
}

afterEach(() => vi.restoreAllMocks());

describe('NodeInviteModal', () => {
  it('sends the form and shows the one-time command with the secret', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue({ success: true, msg: '', obj: [] });
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue({
      success: true,
      msg: '',
      obj: { invite: invite(1, 'pending'), secret: SECRET },
    });
    renderModal();
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByPlaceholderText('e.g. de-frankfurt-1'), {
      target: { value: ' fra-1 ' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Make invitation' }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    const [url, body] = post.mock.calls[0];
    expect(url).toBe('/panel/api/nodes/invites/create');
    expect(body).toMatchObject({ name: 'fra-1', validMinutes: 30, nodeAdmin: false });
    expect((body as { masterUrl: string }).masterUrl).toBe(`${window.location.origin}/`);
    expect(await screen.findByText(nodeEnrollCommand(SECRET))).toBeTruthy();
  });

  it('offers revoke only for a pending invitation and shows where a used one came from', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue({
      success: true,
      msg: '',
      obj: [invite(2, 'used'), invite(3, 'pending')],
    });
    const post = vi
      .spyOn(HttpUtil, 'post')
      .mockResolvedValue({ success: true, msg: '', obj: null });
    renderModal();
    expect(await screen.findByText(/from 203\.0\.113\.7, node #4/)).toBeTruthy();
    const revoke = screen.getAllByRole('button', { name: 'Revoke' });
    expect(revoke).toHaveLength(1);
    fireEvent.click(revoke[0]);
    await waitFor(() => expect(post).toHaveBeenCalledWith('/panel/api/nodes/invites/revoke/3'));
  });
});
