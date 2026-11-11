import { useEffect } from 'react';
import { Form } from 'antd';
import { FormProvider, useForm, type UseFormReturn } from 'react-hook-form';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import AmneziawgFields from '@/pages/inbounds/form/protocols/amneziawg';
import { HttpUtil, Msg } from '@/utils';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

interface FormValues {
  nodeId: number | null;
  settings: {
    server: { kernelMode: boolean; mtu: number; s1: number; i1: string; i2: string; i5: string };
  };
}

function Harness({ onForm }: { onForm: (form: UseFormReturn<FormValues>) => void }) {
  const form = useForm<FormValues>({
    defaultValues: {
      nodeId: 1,
      settings: {
        server: { kernelMode: false, mtu: 1420, s1: 77, i1: '<r 64>', i2: '<r 8>', i5: '<t>' },
      },
    },
  });
  useEffect(() => {
    onForm(form);
  }, [form, onForm]);
  return (
    <FormProvider {...form}>
      <Form>
        <AmneziawgFields
          awgPubKey="public-key"
          regenInboundAwg={() => {}}
          regenInboundAwgObfuscation={() => {}}
        />
      </Form>
    </FormProvider>
  );
}

function renderFields() {
  let form: UseFormReturn<FormValues> | undefined;
  const view = renderWithProviders(<Harness onForm={(f) => (form = f)} />, {
    queryClient: makeTestQueryClient(),
  });
  return { ...view, form: form! };
}

describe('AmneziaWG presets in the inbound form', () => {
  it('replaces only the signature packets with the preset the server built', async () => {
    const quic = '<b 0xc50000000108><r 8><b 0x000044a2><r 1186>';
    const get = vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { i1: quic }));

    const { getByRole, form } = renderFields();
    fireEvent.click(getByRole('button', { name: 'QUIC-like I1' }));

    await waitFor(() => expect(form.getValues('settings.server.i1')).toBe(quic));
    expect(get).toHaveBeenCalledWith('/panel/api/server/getNewAwgSignaturePacket', {
      preset: 'quic',
    });
    expect(form.getValues('settings.server.i2')).toBe('');
    expect(form.getValues('settings.server.i5')).toBe('');
    expect(form.getValues('settings.server.s1')).toBe(77);
  });

  it('keeps the current packets when the server refuses the preset', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(false, 'unknown preset', null));

    const { getByRole, form } = renderFields();
    const button = getByRole('button', { name: 'DNS-like I1' });
    fireEvent.click(button);

    await waitFor(() => expect(button.classList.contains('ant-btn-loading')).toBe(false));
    expect(form.getValues('settings.server.i1')).toBe('<r 64>');
    expect(form.getValues('settings.server.i2')).toBe('<r 8>');
  });

  it('sets the mobile-network MTU on request', () => {
    const { getByRole, form } = renderFields();
    fireEvent.click(getByRole('button', { name: 'Set 1280' }));
    expect(form.getValues('settings.server.mtu')).toBe(1280);
  });
});
