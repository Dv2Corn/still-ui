import { useTranslation } from 'react-i18next';
import { Alert, Modal, Typography } from 'antd';

import { withBasePath } from '@/api/http-init';
import { probeInstallCommand } from '@/lib/probes';

interface Props {
  token: string | null;
  onClose: () => void;
}

// Shown right after a probe is created or its token rotated: the panel keeps
// only a hash, so this is the operator's one chance to copy it.
export default function ProbeTokenModal({ token, onClose }: Props) {
  const { t } = useTranslation();
  const panelUrl = window.location.origin + withBasePath('/');
  return (
    <Modal
      open={token !== null}
      title={t('pages.probes.tokenTitle')}
      onOk={onClose}
      onCancel={onClose}
      okText={t('done')}
      cancelButtonProps={{ style: { display: 'none' } }}
      width={720}
      destroyOnHidden
    >
      <Alert
        type="warning"
        showIcon
        message={t('pages.probes.tokenOnce')}
        style={{ marginBottom: 12 }}
      />
      <Typography.Paragraph strong>{t('pages.probes.installCommand')}</Typography.Paragraph>
      <Typography.Paragraph type="secondary">{t('pages.probes.installHint')}</Typography.Paragraph>
      <Typography.Paragraph copyable code style={{ wordBreak: 'break-all' }}>
        {token ? probeInstallCommand(panelUrl, token) : ''}
      </Typography.Paragraph>
      <Typography.Paragraph strong>{t('pages.probes.token')}</Typography.Paragraph>
      <Typography.Paragraph copyable code style={{ wordBreak: 'break-all' }}>
        {token ?? ''}
      </Typography.Paragraph>
    </Modal>
  );
}
