import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { AllSetting } from '@/models/setting';
import { AllSettingSchema } from '@/schemas/setting';
import DeviceLimitSettings from '@/pages/settings/DeviceLimitSettings';
import SubHero from '@/pages/sub/SubHero';
import { chooseSelectOption, renderWithProviders } from './test-utils';

describe('device limit settings', () => {
  it('edits the panel default, the no-id policy and the expiry', () => {
    const updateSetting = vi.fn();
    renderWithProviders(
      <DeviceLimitSettings allSetting={new AllSetting()} updateSetting={updateSetting} />,
    );
    expect(screen.getByText('Serve, do not count (default)')).toBeTruthy();
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

  it('holds the settings to the ranges the backend accepts', () => {
    const valid = {
      subHwidDefaultLimit: 1000,
      subHwidNoIdPolicy: 'refuse',
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
