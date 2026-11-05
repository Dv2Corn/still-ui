import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';

import VersionModal from '@/pages/index/VersionModal';
import { Status } from '@/models/status';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const OFFICIAL = ['v26.10.31', 'v26.9.9', 'v26.6.27'];

function mockVersions() {
  vi.mocked(HttpUtil.get).mockImplementation(async (url: string) => {
    if (url === '/panel/api/server/getXrayVersion') return new Msg(true, '', OFFICIAL);
    return new Msg(true, '', {});
  });
}

async function renderModal(xray: { version: string; stillBuild: boolean }) {
  mockVersions();
  const onClose = vi.fn();
  const onBusy = vi.fn();
  renderWithProviders(
    <VersionModal
      open
      status={new Status({ xray: { state: 'running', ...xray } })}
      onClose={onClose}
      onBusy={onBusy}
    />,
  );
  await screen.findByRole('radio', { name: 'v26.9.9' });
  return { onClose, onBusy };
}

function installButton(): HTMLButtonElement | null {
  return screen.queryByRole('button', { name: /^Install v/ }) as HTMLButtonElement | null;
}

describe('Xray version dialog', () => {
  it('marks the installed still-ui build and warns only once an official release is picked', async () => {
    await renderModal({ version: '26.10.31', stillBuild: true });

    const still = screen.getByTestId('xray-still-build');
    expect(within(still).getByText('still-ui build')).toBeTruthy();
    expect(within(still).getByText('installed')).toBeTruthy();
    expect(within(still).getByRole('radio')).toHaveProperty('checked', true);
    // The official v26.10.31 is not what runs: the still-ui build reports the same number.
    const officialList = screen.getByTestId('xray-official-list');
    expect(within(officialList).queryByText('installed')).toBeNull();
    expect(screen.getByRole('radio', { name: 'v26.10.31' })).toHaveProperty('checked', false);

    expect(screen.queryByTestId('xray-official-warning')).toBeNull();
    expect(installButton()).toBeNull();

    fireEvent.click(screen.getByRole('radio', { name: 'still-ui build' }));
    expect(screen.queryByTestId('xray-official-warning')).toBeNull();
    expect(screen.queryByText(/cannot install the still-ui build/)).toBeNull();

    fireEvent.click(screen.getByRole('radio', { name: 'v26.10.31' }));
    const warning = await screen.findByTestId('xray-official-warning');
    expect(
      within(warning).getByText('v26.10.31 is an official Xray build, not the still-ui one'),
    ).toBeTruthy();
    expect(within(warning).getByText(/still-ui update/)).toBeTruthy();
    expect(within(warning).getByText(/ios, edge or qq/)).toBeTruthy();
    expect(installButton()?.disabled).toBe(true);
  });

  it('installs an official release only after the admin confirms', async () => {
    const { onClose, onBusy } = await renderModal({ version: '26.10.31', stillBuild: true });
    vi.mocked(HttpUtil.post).mockClear();

    fireEvent.click(screen.getByRole('radio', { name: 'v26.9.9' }));
    await screen.findByTestId('xray-official-warning');
    const button = installButton()!;
    expect(button.textContent).toContain('Install v26.9.9');
    fireEvent.click(button);
    expect(HttpUtil.post).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('checkbox', { name: /I understand/ }));
    expect(installButton()?.disabled).toBe(false);

    // Picking another release takes the confirmation back.
    fireEvent.click(screen.getByRole('radio', { name: 'v26.6.27' }));
    expect(installButton()?.disabled).toBe(true);
    fireEvent.click(screen.getByRole('checkbox', { name: /I understand/ }));

    fireEvent.click(installButton()!);
    await waitFor(() =>
      expect(HttpUtil.post).toHaveBeenCalledWith('/panel/api/server/installXray/v26.6.27'),
    );
    expect(onClose).toHaveBeenCalled();
    expect(onBusy).toHaveBeenCalledWith({ busy: false });
  });

  it('with an official core installed, points back to still-ui update instead of installing', async () => {
    await renderModal({ version: '26.9.9', stillBuild: false });

    const still = screen.getByTestId('xray-still-build');
    expect(within(still).queryByText('installed')).toBeNull();
    const officialList = screen.getByTestId('xray-official-list');
    expect(within(officialList).getAllByText('installed')).toHaveLength(1);
    expect(screen.getByRole('radio', { name: 'v26.9.9' })).toHaveProperty('checked', true);

    fireEvent.click(screen.getByRole('radio', { name: 'still-ui build' }));
    expect(await screen.findByText(/cannot install the still-ui build/)).toBeTruthy();
    expect(screen.queryByTestId('xray-official-warning')).toBeNull();
    expect(installButton()).toBeNull();
  });
});
