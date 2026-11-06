import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import ProbeTokenModal from '@/pages/probes/ProbeTokenModal';

const original = Object.getOwnPropertyDescriptor(window, 'location');

function openedAt(href: string) {
  const url = new URL(href);
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { href, origin: url.origin, protocol: url.protocol, pathname: url.pathname },
  });
}

// The installer refuses an http:// panel (token and inbound keys in clear text); the modal
// must say so instead of handing out a command that only fails on the probe machine.
describe('ProbeTokenModal over plain http', () => {
  afterEach(() => {
    if (original) Object.defineProperty(window, 'location', original);
  });

  it('warns when the panel is open over http://', () => {
    openedAt('http://203.0.113.5:2053/');
    render(<ProbeTokenModal token={'a'.repeat(48)} onClose={() => {}} />);
    expect(screen.getByText(/This panel is open over plain http:\/\//)).toBeTruthy();
    expect(screen.getByText(/--panel 'http:\/\/203\.0\.113\.5:2053\/'/)).toBeTruthy();
  });

  it('shows no warning over https://', () => {
    openedAt('https://panel.example.com:2053/');
    render(<ProbeTokenModal token={'a'.repeat(48)} onClose={() => {}} />);
    expect(screen.queryByText(/This panel is open over plain http:\/\//)).toBeNull();
    expect(screen.getByText(/--panel 'https:\/\/panel\.example\.com:2053\/'/)).toBeTruthy();
  });
});
