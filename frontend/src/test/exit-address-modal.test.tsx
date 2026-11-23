import { describe, it, expect, vi } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import ExitAddressModal from '@/pages/xray/outbounds/ExitAddressModal';
import { renderWithProviders } from './test-utils';

vi.mock('@/api/queries/useInboundOptions', () => ({
  useInboundOptions: () => ({
    data: [
      { id: 1, tag: 'in-443', protocol: 'vless', port: 443, listen: '203.0.113.5' },
      { id: 2, tag: 'in-8443', protocol: 'vless', port: 8443, listen: '' },
    ],
  }),
}));

function selectInbound(tag: string) {
  const multi = document.querySelector('.ant-select-multiple');
  if (!multi) throw new Error('Inbound multi-select not found');
  fireEvent.mouseDown(multi as HTMLElement);
  const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
    (o) => (o.getAttribute('title') ?? o.textContent ?? '').trim() === tag,
  );
  if (!option) throw new Error(`Option '${tag}' not found`);
  fireEvent.click(option);
  fireEvent.keyDown(multi, { key: 'Escape' });
}

function typeIp(ip: string) {
  const input = document.querySelector('.ant-modal input[placeholder="203.0.113.7"]');
  if (!input) throw new Error('IP input not found');
  fireEvent.change(input, { target: { value: ip } });
}

function submit() {
  const btn = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!btn) throw new Error('Primary button not found');
  fireEvent.click(btn);
}

describe('ExitAddressModal', () => {
  it('blocks an exit IP equal to the selected inbound listen address, then accepts another', async () => {
    const onConfirm = vi.fn();
    renderWithProviders(
      <ExitAddressModal
        open
        inboundTags={['in-443', 'in-8443']}
        existingOutboundTags={['direct']}
        onClose={() => {}}
        onConfirm={onConfirm}
      />,
    );

    selectInbound('in-443');
    typeIp('203.0.113.5');
    submit();
    await waitFor(() =>
      expect(document.querySelector('.ant-form-item-has-error')?.textContent).toContain(
        'Clients connect to this address',
      ),
    );
    expect(onConfirm).not.toHaveBeenCalled();

    typeIp('203.0.113.7');
    submit();
    await waitFor(() => expect(onConfirm).toHaveBeenCalledWith('203.0.113.7', ['in-443']));
  });
});
