import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { AllSetting } from '@/models/setting';
import { AllSettingSchema } from '@/schemas/setting';
import DeviceLimitSettings from '@/pages/settings/DeviceLimitSettings';
import SubHero from '@/pages/sub/SubHero';
import ClientHwidListModal from '@/components/clients/ClientHwidList';
import { chooseSelectOption, renderWithProviders } from './test-utils';

describe('device limit settings', () => {
  it('edits the panel default, the no-id policy and the expiry', () => {
    const updateSetting = vi.fn();
    renderWithProviders(
      <DeviceLimitSettings allSetting={new AllSetting()} updateSetting={updateSetting} />,
    );
    expect(screen.getByText('One shared slot for all of them (default)')).toBeTruthy();
    chooseSelectOption('sub-hwid-no-id-policy', 'Refuse with a notice');
    expect(updateSetting).toHaveBeenCalledWith({ subHwidNoIdPolicy: 'refuse' });

    const limit = screen.getByRole('spinbutton', { name: 'Default device limit' });
    expect((limit as HTMLInputElement).value).toBe('0');
    fireEvent.change(limit, { target: { value: '3' } });
    expect(updateSetting).toHaveBeenCalledWith({ subHwidDefaultLimit: 3 });

    const days = screen.getByRole('spinbutton', { name: 'Forget a device after, days' });
    expect((days as HTMLInputElement).value).toBe('30');
    fireEvent.change(days, { target: { value: '7' } });
    expect(updateSetting).toHaveBeenCalledWith({ subHwidExpireDays: 7 });
  });

  // The translation layer interpolates single braces; the token must reach the operator intact.
  it('names the limit token in the notice description', () => {
    renderWithProviders(
      <DeviceLimitSettings allSetting={new AllSetting()} updateSetting={vi.fn()} />,
    );
    expect(screen.getByText(/\{\{LIMIT\}\} is the limit/)).toBeTruthy();
  });

  // "Serve, do not count" lets any app that drops the header past the limit; the admin must see that.
  it('warns that the limit can be bypassed only while apps without an id are not counted', () => {
    const { unmount } = renderWithProviders(
      <DeviceLimitSettings
        allSetting={new AllSetting({ subHwidNoIdPolicy: 'allow' })}
        updateSetting={vi.fn()}
      />,
    );
    expect(screen.getByText(/a device limit can be bypassed/)).toBeTruthy();
    unmount();
    for (const policy of ['shared', 'refuse']) {
      const view = renderWithProviders(
        <DeviceLimitSettings
          allSetting={new AllSetting({ subHwidNoIdPolicy: policy })}
          updateSetting={vi.fn()}
        />,
      );
      expect(screen.queryByText(/a device limit can be bypassed/), policy).toBeNull();
      view.unmount();
    }
  });

  it('holds the settings to the ranges the backend accepts', () => {
    const valid = {
      subHwidDefaultLimit: 1000,
      subHwidNoIdPolicy: 'shared',
      subHwidExpireDays: 3650,
      subHwidLimitNotice: 'x'.repeat(256),
    };
    expect(AllSettingSchema.safeParse(valid).success).toBe(true);
    for (const bad of [
      { subHwidDefaultLimit: -1 },
      { subHwidDefaultLimit: 1001 },
      { subHwidNoIdPolicy: 'block' },
      { subHwidExpireDays: 3651 },
      { subHwidLimitNotice: 'x'.repeat(257) },
      { subHwidNoIdNotice: 'x'.repeat(257) },
    ]) {
      expect(AllSettingSchema.safeParse(bad).success, JSON.stringify(bad)).toBe(false);
    }
  });
});

describe('client device list', () => {
  const entry = {
    id: 1,
    firstSeen: 1,
    lastSeen: 2,
    userAgent: 'v2rayNG/1.10',
    deviceOs: '',
    osVersion: '',
    deviceModel: '',
    fingerprint: '',
  };
  const modal = (noId: boolean) => (
    <ClientHwidListModal
      open
      hwids={[{ ...entry, noId }]}
      loading={false}
      clearing={false}
      deletingId={null}
      formatDate={String}
      onRefresh={vi.fn()}
      onClearAll={vi.fn()}
      onDelete={vi.fn()}
      onClose={vi.fn()}
    />
  );

  // Without the label the admin sees "v2rayNG" holding a slot and cannot tell it is every such app.
  it('names the slot that apps without a device id share', () => {
    const { unmount } = renderWithProviders(modal(true));
    expect(screen.getByText('Apps without a device ID (one shared slot)')).toBeTruthy();
    unmount();
    renderWithProviders(modal(false));
    expect(screen.queryByText('Apps without a device ID (one shared slot)')).toBeNull();
  });
});

describe('subscription page device count', () => {
  const hero = {
    status: 'active' as const,
    daysLeft: null,
    usedByte: 0,
    totalByte: 0,
    expireMs: 0,
    lastOnlineMs: 0,
    download: '0',
    upload: '0',
    used: '0',
    total: '∞',
    remained: '',
    datepicker: 'gregorian' as const,
    lang: 'en-US',
  };

  it('shows registered devices against the limit', () => {
    renderWithProviders(<SubHero {...hero} deviceLimit={3} deviceCount={2} />);
    expect(screen.getByText('Devices')).toBeTruthy();
    expect(screen.getByText('2 / 3')).toBeTruthy();
  });

  it('says nothing about devices without a limit', () => {
    renderWithProviders(<SubHero {...hero} />);
    expect(screen.queryByText('Devices')).toBeNull();
  });
});
