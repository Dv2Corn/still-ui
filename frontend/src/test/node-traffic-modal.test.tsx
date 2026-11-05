import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import NodeList from '@/pages/nodes/NodeList';
import NodeTrafficModal from '@/pages/nodes/NodeTrafficModal';
import type { NodeRecord } from '@/schemas/node';
import { Msg } from '@/utils';

import { renderWithProviders } from './test-utils';

const noop = () => {};
const GB = 1024 ** 3;

const node: NodeRecord = {
  id: 7,
  name: 'edge',
  enable: true,
  status: 'online',
  trafficLimitBytes: 100 * GB,
  trafficUsed: 85 * GB,
  trafficWarnPercent: 80,
  trafficResetDay: 31,
  trafficLimitAction: 'notify',
  trafficMultiplier: 1,
  trafficNextReset: Date.UTC(2026, 10, 30),
};

describe('node traffic limit UI', () => {
  it('shows the usage against the limit and opens the editor from the row', () => {
    const onTrafficLimit = vi.fn();
    renderWithProviders(
      <NodeList
        nodes={[node, { ...node, id: 8, name: 'free', trafficLimitBytes: 0, trafficUsed: 0 }]}
        isMobile={false}
        selectedIds={[]}
        onSelectionChange={noop}
        onAdd={noop}
        onInvite={noop}
        onMtls={noop}
        onEdit={noop}
        onDelete={noop}
        onProbe={noop}
        onToggleEnable={noop}
        onTrafficLimit={onTrafficLimit}
        onUpdateNode={noop}
        onUpdateSelected={noop}
      />,
    );
    const row = screen.getByText('edge').closest('tr') as HTMLElement;
    expect(row.textContent).toContain('85.00 GB of 100.00 GB');
    const free = screen.getByText('free').closest('tr') as HTMLElement;
    expect(free.textContent).toContain('no limit');

    fireEvent.click(screen.getAllByRole('button', { name: 'Traffic limit' })[0]);
    expect(onTrafficLimit).toHaveBeenCalledWith(expect.objectContaining({ id: 7 }));
  });

  it('warns before turning inbounds off and saves the limit in bytes', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, ''));
    const onClose = vi.fn();
    renderWithProviders(<NodeTrafficModal node={node} save={save} onClose={onClose} />);

    expect(screen.queryByText(/loses access until the reset day/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Turn off the node's inbounds"));
    expect(screen.getByText(/loses access until the reset day/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: /Save/ }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save).toHaveBeenCalledWith(7, {
      trafficLimitBytes: 100 * GB,
      trafficResetDay: 31,
      trafficWarnPercent: 80,
      trafficLimitAction: 'disable',
      trafficMultiplier: 1,
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});
