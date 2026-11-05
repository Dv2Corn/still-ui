import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { FormProvider, useForm } from 'react-hook-form';

import { withBasePath } from '@/api/http-init';
import {
  useNodeInviteMutations,
  useNodeInvitesQuery,
  type NodeInviteView,
} from '@/api/queries/useNodeInvites';
import { FormField } from '@/components/form/rhf';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { nodeEnrollCommand } from '@/lib/node-enroll';
import { IntlUtil } from '@/utils';

interface Props {
  open: boolean;
  onClose: () => void;
}

interface InviteFormValues {
  name: string;
  address: string;
  allowPrivateAddress: boolean;
  nodeAdmin: boolean;
  validMinutes: number;
  masterUrl: string;
}

const STATUS_COLORS: Record<string, string> = {
  pending: 'processing',
  used: 'success',
  expired: 'default',
  revoked: 'default',
};

function defaults(): InviteFormValues {
  return {
    name: '',
    address: '',
    allowPrivateAddress: false,
    nodeAdmin: false,
    validMinutes: 30,
    masterUrl: window.location.origin + withBasePath('/'),
  };
}

// The invitation secret exists only in the create answer: once the dialog
// closes, the panel keeps nothing but its hash.
export default function NodeInviteModal({ open, onClose }: Props) {
  const { t } = useTranslation();
  const { isMobile } = useMediaQuery();
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ value: string; expiresAt: number } | null>(null);
  const methods = useForm<InviteFormValues>({ defaultValues: defaults() });
  const { data: invites = [], isFetching } = useNodeInvitesQuery(open);
  const { create, revoke } = useNodeInviteMutations();

  const close = () => {
    methods.reset(defaults());
    setSecret(null);
    onClose();
  };

  const onCreate = async (values: InviteFormValues) => {
    if (creating) return;
    setCreating(true);
    try {
      const msg = await create({
        name: values.name.trim(),
        address: values.address.trim(),
        allowPrivateAddress: values.allowPrivateAddress,
        nodeAdmin: values.nodeAdmin,
        validMinutes: values.validMinutes,
        masterUrl: values.masterUrl.trim(),
      });
      if (msg?.success && msg.obj) {
        setSecret({ value: msg.obj.secret, expiresAt: msg.obj.invite.expiresAt });
      }
    } finally {
      setCreating(false);
    }
  };

  const columns: ColumnsType<NodeInviteView> = [
    { title: t('pages.nodes.name'), dataIndex: 'name', render: (v: string) => v || '—' },
    {
      title: t('pages.nodes.status'),
      dataIndex: 'status',
      render: (s: string, r) => (
        <Tag color={STATUS_COLORS[s]} title={r.lastError || undefined}>
          {t(`pages.nodes.invite.status.${s}`)}
        </Tag>
      ),
    },
    {
      title: t('pages.nodes.invite.details'),
      key: 'details',
      render: (_, r) =>
        r.status === 'used'
          ? t('pages.nodes.invite.usedFrom', {
              ip: r.usedFromIp,
              id: r.nodeId,
              time: IntlUtil.formatDate(r.usedAt * 1000),
            })
          : t('pages.nodes.invite.validUntil', { time: IntlUtil.formatDate(r.expiresAt * 1000) }),
    },
    {
      title: '',
      key: 'actions',
      width: 100,
      render: (_, r) =>
        r.status === 'pending' ? (
          <Button size="small" danger onClick={() => revoke(r.id)}>
            {t('pages.nodes.invite.revoke')}
          </Button>
        ) : null,
    },
  ];

  return (
    <Modal
      open={open}
      title={t('pages.nodes.invite.title')}
      onCancel={close}
      footer={null}
      destroyOnHidden
      width={isMobile ? '95vw' : 760}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t('pages.nodes.invite.intro')}
      </Typography.Paragraph>
      {secret ? (
        <>
          <Alert
            type="warning"
            showIcon
            message={t('pages.nodes.invite.once', {
              time: IntlUtil.formatDate(secret.expiresAt * 1000),
            })}
            style={{ marginBottom: 12 }}
          />
          <Typography.Paragraph strong>{t('pages.nodes.invite.command')}</Typography.Paragraph>
          <Typography.Paragraph copyable code style={{ wordBreak: 'break-all' }}>
            {nodeEnrollCommand(secret.value)}
          </Typography.Paragraph>
          <Button onClick={() => setSecret(null)}>{t('pages.nodes.invite.another')}</Button>
        </>
      ) : (
        <FormProvider {...methods}>
          <Form
            colon={false}
            labelCol={{ sm: { span: 8 } }}
            wrapperCol={{ sm: { span: 15 } }}
            labelWrap
          >
            <FormField name="name" label={t('pages.nodes.name')}>
              <Input maxLength={64} placeholder={t('pages.nodes.namePlaceholder')} />
            </FormField>
            <FormField
              name="address"
              label={t('pages.nodes.address')}
              tooltip={t('pages.nodes.invite.addressHint')}
            >
              <Input maxLength={253} placeholder={t('pages.nodes.invite.addressPlaceholder')} />
            </FormField>
            <FormField
              name="masterUrl"
              label={t('pages.nodes.invite.masterUrl')}
              tooltip={t('pages.nodes.invite.masterUrlHint')}
            >
              <Input maxLength={512} />
            </FormField>
            <FormField name="validMinutes" label={t('pages.nodes.invite.validMinutes')}>
              <InputNumber min={5} max={1440} />
            </FormField>
            <FormField
              name="nodeAdmin"
              label={t('pages.nodes.invite.nodeAdmin')}
              tooltip={t('pages.nodes.invite.nodeAdminHint')}
              valueProp="checked"
            >
              <Switch />
            </FormField>
            <FormField
              name="allowPrivateAddress"
              label={t('pages.nodes.allowPrivateAddress')}
              tooltip={t('pages.nodes.allowPrivateAddressHint')}
              valueProp="checked"
            >
              <Switch />
            </FormField>
            <Button
              type="primary"
              loading={creating}
              onClick={methods.handleSubmit(onCreate)}
              block
            >
              {t('pages.nodes.invite.create')}
            </Button>
          </Form>
        </FormProvider>
      )}
      <Typography.Title level={5} style={{ marginTop: 20 }}>
        {t('pages.nodes.invite.list')}
      </Typography.Title>
      <Table
        size="small"
        rowKey="id"
        columns={columns}
        dataSource={invites}
        loading={isFetching && invites.length === 0}
        pagination={{ pageSize: 5, hideOnSinglePage: true }}
        scroll={{ x: 'max-content' }}
      />
    </Modal>
  );
}
