import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import SubLocalDirect from '@/pages/sub/SubLocalDirect';
import { renderWithProviders } from './test-utils';

const confUrl = 'https://sub.example.com/sub/s1/shadowrocket.conf';

describe('SubLocalDirect', () => {
  it('names the country and offers the Shadowrocket rules file', () => {
    const onCopy = vi.fn();
    renderWithProviders(
      <SubLocalDirect country="ru" shadowrocketConfUrl={confUrl} lang="en-US" onCopy={onCopy} />,
    );

    expect(screen.getByText('Russia: local sites open directly')).toBeTruthy();
    const file = screen.getByRole('link', { name: 'Shadowrocket rules file' });
    expect(file.getAttribute('href')).toBe(confUrl);
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(onCopy).toHaveBeenCalledWith(confUrl);
  });

  it('shows the mode without file controls when the panel sent no file link', () => {
    renderWithProviders(
      <SubLocalDirect country="ir" shadowrocketConfUrl="" lang="en-US" onCopy={vi.fn()} />,
    );

    expect(screen.getByText('Iran: local sites open directly')).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Shadowrocket rules file' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
  });
});
