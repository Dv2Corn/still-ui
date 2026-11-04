import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Form, Input, InputNumber, Modal, Select, Switch } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { ReachProbeInput, ReachProbeView } from '@/api/queries/useReachProbes';
import type { InboundOption } from '@/schemas/client';
import { ReachProbeFormSchema, type ReachProbeFormValues } from '@/schemas/api/reachProbe';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { selectTagRender } from '@/components/form';
import { PROBE_PROTOCOLS } from '@/lib/probes';
import { useMediaQuery } from '@/hooks/useMediaQuery';

const DEFAULT_TEST_URL = 'https://speed.cloudflare.com/__down?bytes=262144';

interface Props {
  open: boolean;
  probe: ReachProbeView | null;
  inboundOptions: InboundOption[];
  save: (input: ReachProbeInput) => Promise<{ success?: boolean } | undefined>;
  onOpenChange: (open: boolean) => void;
}

function defaultsFor(
  probe: ReachProbeView | null,
  inbounds: InboundOption[],
): ReachProbeFormValues {
  return {
    name: probe?.name ?? '',
    enabled: probe?.enabled ?? true,
    inboundIds:
      probe?.inboundIds ??
      inbounds
        .filter((ib) => ib.enable && PROBE_PROTOCOLS.includes(ib.protocol ?? ''))
        .map((ib) => ib.id),
    testUrl: probe?.testUrl ?? DEFAULT_TEST_URL,
    directUrl: probe?.directUrl ?? '',
    intervalMin: Math.round((probe?.intervalSec ?? 600) / 60),
  };
}

export default function ProbeFormModal({ open, probe, inboundOptions, save, onOpenChange }: Props) {
  const { t } = useTranslation();
  const { isMobile } = useMediaQuery();
  const [loading, setLoading] = useState(false);
  const methods = useForm<ReachProbeFormValues>({
    defaultValues: defaultsFor(probe, inboundOptions),
  });

  useEffect(() => {
    if (open) methods.reset(defaultsFor(probe, inboundOptions));
  }, [open, probe, inboundOptions, methods]);

  const options = useMemo(
    () =>
      inboundOptions
        .filter((ib) => PROBE_PROTOCOLS.includes(ib.protocol ?? ''))
        .map((ib) => ({
          value: ib.id,
          label: `${ib.remark || ib.tag || `#${ib.id}`} (${ib.protocol}, ${ib.port})`,
        })),
    [inboundOptions],
  );

  const onFinish = async (values: ReachProbeFormValues) => {
    if (loading) return;
    setLoading(true);
    try {
      const res = await save({
        name: values.name.trim(),
        enabled: values.enabled,
        inboundIds: values.inboundIds,
        testUrl: values.testUrl.trim(),
        directUrl: values.directUrl.trim(),
        intervalSec: values.intervalMin * 60,
      });
      if (res?.success) onOpenChange(false);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      open={open}
      title={t(probe ? 'pages.probes.editProbe' : 'pages.probes.addProbe')}
      onOk={methods.handleSubmit(onFinish)}
      onCancel={() => onOpenChange(false)}
      confirmLoading={loading}
      okText={t('save')}
      cancelText={t('cancel')}
      destroyOnHidden
      width={isMobile ? '95vw' : 640}
    >
      <FormProvider {...methods}>
        <Form
          colon={false}
          labelCol={{ sm: { span: 8 } }}
          wrapperCol={{ sm: { span: 15 } }}
          labelWrap
        >
          <FormField
            name="name"
            label={t('pages.probes.fields.name')}
            tooltip={t('pages.probes.hints.name')}
            rules={{ validate: rhfZodValidate(ReachProbeFormSchema.shape.name) }}
          >
            <Input maxLength={64} />
          </FormField>
          {probe && (
            <FormField name="enabled" label={t('enable')} valueProp="checked">
              <Switch />
            </FormField>
          )}
          <FormField
            name="inboundIds"
            label={t('pages.probes.fields.inbounds')}
            tooltip={t('pages.probes.hints.inbounds')}
            rules={{ validate: rhfZodValidate(ReachProbeFormSchema.shape.inboundIds) }}
          >
            <Select mode="multiple" tagRender={selectTagRender} options={options} />
          </FormField>
          <FormField
            name="testUrl"
            label={t('pages.probes.fields.testUrl')}
            tooltip={t('pages.probes.hints.testUrl')}
            rules={{ validate: rhfZodValidate(ReachProbeFormSchema.shape.testUrl) }}
          >
            <Input maxLength={512} placeholder={DEFAULT_TEST_URL} />
          </FormField>
          <FormField
            name="directUrl"
            label={t('pages.probes.fields.directUrl')}
            tooltip={t('pages.probes.hints.directUrl')}
            rules={{ validate: rhfZodValidate(ReachProbeFormSchema.shape.directUrl) }}
          >
            <Input maxLength={512} placeholder="https://example.com/" />
          </FormField>
          <FormField
            name="intervalMin"
            label={t('pages.probes.fields.interval')}
            rules={{ validate: rhfZodValidate(ReachProbeFormSchema.shape.intervalMin) }}
          >
            <InputNumber min={1} max={1440} addonAfter={t('pages.probes.minutes')} />
          </FormField>
        </Form>
      </FormProvider>
    </Modal>
  );
}
