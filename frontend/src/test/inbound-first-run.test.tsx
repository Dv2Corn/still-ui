import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';

import { InboundList } from '@/pages/inbounds/list';
import type { DBInboundRecord } from '@/pages/inbounds/list/types';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';

import { renderWithProviders } from './test-utils';

const HEADLINE = 'Start with a ready-made inbound';

function renderList(dbInbounds: DBInboundRecord[], isMobile = false) {
  const onAddInbound = vi.fn();
  renderWithProviders(
    <MemoryRouter>
      <InboundList
        dbInbounds={dbInbounds}
        clientCount={{}}
        onlineClients={[]}
        lastOnlineMap={{}}
        inboundSpeed={{}}
        expireDiff={0}
        trafficDiff={0}
        pageSize={0}
        isMobile={isMobile}
        subEnable={false}
        nodesById={new Map()}
        hasActiveNode={false}
        hosts={[]}
        onAddInbound={onAddInbound}
        onGeneralAction={() => {}}
        onRowAction={() => {}}
        onBulkDelete={async () => true}
      />
    </MemoryRouter>,
  );
  return onAddInbound;
}

const oneInbound = {
  id: 1,
  enable: true,
  remark: 'existing',
  subSortIndex: 0,
  port: 443,
  protocol: 'vless',
  up: 0,
  down: 0,
  total: 0,
  expiryTime: 0,
  _expiryTime: null,
  settings: {},
  streamSettings: {},
  isVLess: true,
} satisfies DBInboundRecord;

describe('first-run presets on an empty panel', () => {
  it.each([false, true])('offers every preset when there are no inbounds (mobile %s)', (mobile) => {
    const onAddInbound = renderList([], mobile);

    expect(screen.getByText(HEADLINE)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /Hysteria2 \(UDP\)/ }));
    expect(onAddInbound).toHaveBeenLastCalledWith('ruMobileHysteria');
    fireEvent.click(screen.getByRole('button', { name: /VLESS XHTTP REALITY/ }));
    expect(onAddInbound).toHaveBeenLastCalledWith('ruMobileXhttp');
    fireEvent.click(screen.getByRole('button', { name: 'Set up manually' }));
    expect(onAddInbound).toHaveBeenLastCalledWith();
  });

  it('keeps the plain table once an inbound exists', () => {
    renderList([oneInbound]);

    expect(screen.getByText('existing')).toBeTruthy();
    expect(screen.queryByText(HEADLINE)).toBeNull();
  });

  // The toolbar button passed its click event through, which would read as a preset.
  it('opens the toolbar Add Inbound without a preset', () => {
    const onAddInbound = renderList([oneInbound]);

    fireEvent.click(screen.getByRole('button', { name: 'Add Inbound' }));
    expect(onAddInbound).toHaveBeenLastCalledWith();
  });

  it('opens the Add Inbound modal with the chosen preset applied', async () => {
    renderWithProviders(
      <InboundFormModal
        open
        mode="add"
        initialPreset="ruMobileHysteria"
        dbInbound={null}
        dbInbounds={[]}
        availableNodes={[]}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole('tab', { name: 'Security' }).getAttribute('aria-selected')).toBe(
        'true',
      ),
    );
    expect(screen.getByText(/UDP 443 often keeps working in whitelist mode/)).toBeTruthy();
  });
});
