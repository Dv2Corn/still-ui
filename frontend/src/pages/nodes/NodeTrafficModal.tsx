import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Form, InputNumber, Modal, Radio, Typography } from 'antd';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { NodeTrafficLimitPayload } from '@/api/queries/useNodeMutations';
import type { Msg } from '@/utils';
import { bytesToGb, gbToBytes, trafficFormValues } from './nodeTraffic';

interface NodeTrafficModalProps {
  node: NodeRecord | null;
  save: (id: number, payload: NodeTrafficLimitPayload) => Promise<Msg<unknown>>;
  onClose: () => void;
}

// The form starts from the node it opens on; the page remounts it per node (key).
export default function NodeTrafficModal({ node, save, onClose }: NodeTrafficModalProps) {
  const { t } = useTranslation();
  const [values, setValues] = useState<NodeTrafficLimitPayload>(() => trafficFormValues(node));
  const [saving, setSaving] = useState(false);

  const patch = (p: Partial<NodeTrafficLimitPayload>) => setValues((v) => ({ ...v, ...p }));

  async function onOk() {
    if (!node) return;
    setSaving(true);
    try {
      const msg = await save(node.id, values);
      if (msg?.success) onClose();
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={!!node}
      title={`${t('pages.nodes.trafficLimit')}${node?.name ? ` — ${node.name}` : ''}`}
      okText={t('save')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      onOk={onOk}
      onCancel={onClose}
      destroyOnHidden
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t('pages.nodes.trafficLimitHint')}
      </Typography.Paragraph>
      <Form layout="vertical">
        <Form.Item label={t('pages.nodes.trafficLimitGB')}>
          <InputNumber
            aria-label={t('pages.nodes.trafficLimitGB')}
            min={0}
            step={1}
            precision={2}
            style={{ width: '100%' }}
            value={bytesToGb(values.trafficLimitBytes)}
            onChange={(v) => patch({ trafficLimitBytes: gbToBytes(v) })}
          />
        </Form.Item>
        <Form.Item
          label={t('pages.nodes.trafficResetDay')}
          extra={t('pages.nodes.trafficResetDayHint')}
        >
          <InputNumber
            aria-label={t('pages.nodes.trafficResetDay')}
            min={1}
            max={31}
            precision={0}
            style={{ width: '100%' }}
            value={values.trafficResetDay}
            onChange={(v) => patch({ trafficResetDay: v ?? 1 })}
          />
        </Form.Item>
        <Form.Item
          label={t('pages.nodes.trafficWarnPercent')}
          extra={t('pages.nodes.trafficWarnPercentHint')}
        >
          <InputNumber
            aria-label={t('pages.nodes.trafficWarnPercent')}
            min={0}
            max={100}
            precision={0}
            style={{ width: '100%' }}
            value={values.trafficWarnPercent}
            onChange={(v) => patch({ trafficWarnPercent: v ?? 0 })}
          />
        </Form.Item>
        <Form.Item label={t('pages.nodes.trafficLimitAction')}>
          <Radio.Group
            value={values.trafficLimitAction}
            onChange={(e) => patch({ trafficLimitAction: e.target.value })}
          >
            <Radio value="notify">{t('pages.nodes.trafficActionNotify')}</Radio>
            <Radio value="disable">{t('pages.nodes.trafficActionDisable')}</Radio>
          </Radio.Group>
          {values.trafficLimitAction === 'disable' && (
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 8 }}
              title={t('pages.nodes.trafficActionDisableWarning')}
            />
          )}
        </Form.Item>
        <Form.Item
          label={t('pages.nodes.trafficMultiplier')}
          extra={t('pages.nodes.trafficMultiplierHint')}
        >
          <InputNumber
            aria-label={t('pages.nodes.trafficMultiplier')}
            min={0.1}
            max={100}
            step={0.1}
            precision={2}
            style={{ width: '100%' }}
            value={values.trafficMultiplier}
            onChange={(v) => patch({ trafficMultiplier: v ?? 1 })}
          />
        </Form.Item>
      </Form>
      <Typography.Text type="secondary">{t('pages.nodes.trafficAlertHint')}</Typography.Text>
    </Modal>
  );
}
