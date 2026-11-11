import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import ImportTab from '@/pages/settings/ImportTab';
import type { PanelImportReport } from '@/generated/types';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

function report(patch: Partial<PanelImportReport> = {}): PanelImportReport {
  return {
    source: 'marzban',
    applied: false,
    notEmpty: true,
    inbounds: [
      { tag: 'VLESS TCP REALITY', protocol: 'vless', port: 443, clients: 5, hosts: 1, skipped: '' },
      {
        tag: 'VLESS WS TLS',
        protocol: 'vless',
        port: 8443,
        clients: 0,
        hosts: 0,
        skipped: 'port 8443 is taken',
      },
    ],
    clients: 5,
    legacySubLinks: 5,
    notes: ['user "erin": the yearly traffic reset has no equivalent, set to never'],
    admins: ['root'],
    backup: '',
    ...patch,
  };
}

function chooseFile(container: HTMLElement, index: number, name: string) {
  const input = container.querySelectorAll('input[type="file"]')[index] as HTMLInputElement;
  fireEvent.change(input, { target: { files: [new File(['x'], name)] } });
}

function sent(call: unknown[]): Record<string, string> {
  const out: Record<string, string> = {};
  (call[1] as FormData).forEach((value, key) => {
    out[key] = value instanceof File ? value.name : value;
  });
  return out;
}

describe('ImportTab', () => {
  it('checks first, and imports into a panel with inbounds only after merge is ticked', async () => {
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    post.mockResolvedValueOnce(new Msg(true, '', report()));
    const { container } = renderWithProviders(<ImportTab />);

    const check = screen.getByRole('button', { name: 'Check' });
    expect(check.hasAttribute('disabled')).toBe(true);
    chooseFile(container, 0, 'db.sqlite3');
    expect(check.hasAttribute('disabled')).toBe(true);
    chooseFile(container, 1, 'xray_config.json');
    await waitFor(() => expect(check.hasAttribute('disabled')).toBe(false));
    fireEvent.click(check);

    await screen.findByText('not imported: port 8443 is taken');
    expect(post.mock.calls[0][0]).toBe('/panel/api/server/importPanel');
    expect(sent(post.mock.calls[0])).toEqual({
      source: 'marzban',
      db: 'db.sqlite3',
      config: 'xray_config.json',
      apply: 'false',
      merge: 'false',
    });
    screen.getByText('Admins are not imported: root');
    screen.getByText(/the yearly traffic reset has no equivalent/);

    const apply = screen.getByRole('button', { name: 'Import' });
    expect(apply.hasAttribute('disabled')).toBe(true);
    fireEvent.click(screen.getByRole('checkbox'));
    expect(apply.hasAttribute('disabled')).toBe(false);

    post.mockResolvedValueOnce(
      new Msg(true, '', report({ applied: true, backup: '/etc/still-ui/backups/b.db' })),
    );
    fireEvent.click(apply);
    await screen.findByText('Imported. A backup was taken first: /etc/still-ui/backups/b.db');
    expect(sent(post.mock.calls[1])).toMatchObject({ apply: 'true', merge: 'true' });
    expect(screen.queryByRole('button', { name: 'Import' })).toBeNull();
  });

  it('asks an x-ui import for the database only', async () => {
    const post = vi.mocked(HttpUtil.post);
    post.mockClear();
    post.mockResolvedValueOnce(new Msg(true, '', report({ source: 'x-ui', notEmpty: false })));
    const { container } = renderWithProviders(<ImportTab />);

    fireEvent.click(screen.getByRole('radio', { name: 'x-ui / 3x-ui before v3' }));
    expect(container.querySelectorAll('input[type="file"]').length).toBe(1);
    chooseFile(container, 0, 'x-ui.db');
    const check = screen.getByRole('button', { name: 'Check' });
    await waitFor(() => expect(check.hasAttribute('disabled')).toBe(false));
    fireEvent.click(check);

    await screen.findByText('What will be imported');
    expect(sent(post.mock.calls[0])).toEqual({
      source: 'x-ui',
      db: 'x-ui.db',
      apply: 'false',
      merge: 'false',
    });
    expect(screen.getByRole('button', { name: 'Import' }).hasAttribute('disabled')).toBe(false);
  });
});
