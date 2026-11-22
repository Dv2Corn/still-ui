import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Form, Input, Modal, Select } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import { selectTagRender } from '@/components/form';
import { FormField } from '@/components/form/rhf';
import { useInboundOptions } from '@/api/queries/useInboundOptions';
import { exitAddressError } from '@/lib/xray/exit-address-preset';

interface ExitAddressForm {
  ip: string;
  inboundTags: string[];
}

interface ExitAddressModalProps {
  open: boolean;
  inboundTags: string[];
  existingOutboundTags: string[];
  onClose: () => void;
  onConfirm: (ip: string, inboundTags: string[]) => void;
}

export default function ExitAddressModal({
  open,
  inboundTags,
  existingOutboundTags,
  onClose,
  onConfirm,
}: ExitAddressModalProps) {
  const { t } = useTranslation();
  const { data: inboundOptions = [] } = useInboundOptions();
  const methods = useForm<ExitAddressForm>({ defaultValues: { ip: '', inboundTags: [] } });

  useEffect(() => {
    if (open) methods.reset({ ip: '', inboundTags: [] });
  }, [open, methods]);

  function validateIp(ip: string | string[], values: ExitAddressForm): string | true {
    const selected = inboundOptions.filter((ib) => ib.tag && values.inboundTags.includes(ib.tag));
    return exitAddressError(String(ip).trim(), selected, existingOutboundTags) ?? true;
  }

  return (
    <Modal
      open={open}
      title={t('pages.xray.exitAddress.title')}
      okText={t('confirm')}
      cancelText={t('close')}
      mask={{ closable: false }}
      onOk={methods.handleSubmit((values) => onConfirm(values.ip.trim(), values.inboundTags))}
      onCancel={onClose}
    >
      <Alert
        type="info"
        showIcon
        title={t('pages.xray.exitAddress.intro')}
        style={{ marginBottom: 16 }}
      />
      <FormProvider {...methods}>
        <Form layout="vertical">
          <FormField
            label={t('pages.xray.exitAddress.inbounds')}
            name="inboundTags"
            rules={{
              validate: (tags: string | string[]) =>
                tags.length > 0 || 'pages.xray.exitAddress.inboundsRequired',
              deps: ['ip'],
            }}
          >
            <Select
              mode="multiple"
              tagRender={selectTagRender}
              options={inboundTags.map((tag) => ({ value: tag, label: tag }))}
            />
          </FormField>
          <FormField
            label={t('pages.xray.exitAddress.exitIp')}
            extra={t('pages.xray.exitAddress.exitIpHint')}
            name="ip"
            rules={{ validate: validateIp }}
          >
            <Input placeholder="203.0.113.7" />
          </FormField>
        </Form>
      </FormProvider>
    </Modal>
  );
}
