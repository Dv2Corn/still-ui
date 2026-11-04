import { describe, expect, it, onTestFinished, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import TorrentBlockSetting from '@/pages/xray/routing/TorrentBlockSetting';
import type { TorrentBlockStatus } from '@/api/queries/useTorrentBlockQuery';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const zero = {
  handshakes: 0,
  dht: 0,
  trackers: 0,
  utp: 0,
  peerDrops: 0,
  tunnelBans: 0,
};

function serveStatus(status: TorrentBlockStatus) {
  const get = vi.mocked(HttpUtil.get);
  const fallback = get.getMockImplementation()!;
  get.mockImplementation(async (url, ...rest) =>
    url === '/panel/api/server/torrentBlock' ? new Msg(true, '', status) : fallback(url, ...rest),
  );
  onTestFinished(() => {
    get.mockImplementation(fallback);
  });
}

describe('TorrentBlockSetting', () => {
  it('turns blocking on and says when nft is missing', async () => {
    serveStatus({
      enabled: false,
      supported: true,
      nftInstalled: false,
      active: false,
      counters: zero,
    });
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    post.mockResolvedValueOnce(
      new Msg(true, '', {
        enabled: true,
        supported: true,
        nftInstalled: false,
        active: false,
        counters: zero,
        error: 'nft is not installed',
      }),
    );
    renderWithProviders(<TorrentBlockSetting />);

    const toggle = await screen.findByRole('switch', {
      name: 'Block torrents for clients',
    });
    await waitFor(() => expect(toggle.hasAttribute('disabled')).toBe(false));
    expect(screen.queryByText(/nft is not installed/)).toBeNull();
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/server/setTorrentBlock', {
        enable: true,
      }),
    );
    await screen.findByText(/nft is not installed: only traffic through Xray is blocked/);
    expect(toggle.getAttribute('aria-checked')).toBe('true');
  });

  it('shows what the loaded filter has caught', async () => {
    serveStatus({
      enabled: true,
      supported: true,
      nftInstalled: true,
      active: true,
      counters: {
        handshakes: 3,
        dht: 61,
        trackers: 2,
        utp: 276,
        peerDrops: 59811,
        tunnelBans: 7,
      },
    });
    renderWithProviders(<TorrentBlockSetting />);

    await screen.findByText(
      'Since the filter loaded: 342 torrent packets caught, 59818 packets to known peers dropped.',
    );
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
