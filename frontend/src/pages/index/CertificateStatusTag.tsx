import { useTranslation } from 'react-i18next';
import { Tag, Tooltip } from 'antd';
import { SafetyCertificateOutlined } from '@ant-design/icons';

import { IntlUtil } from '@/utils';
import { useDatepicker } from '@/hooks/useDatepicker';
import type { XrayCertificate } from '@/models/status';

const STATE_KEYS: Record<string, string> = {
  ok: 'pages.index.certStateOk',
  renewed: 'pages.index.certStateRenewed',
  expiring: 'pages.index.certStateExpiring',
  expired: 'pages.index.certStateExpired',
  unreachable: 'pages.index.certStateUnreachable',
};

// Worst state first: the tag's colour follows the inbound that needs attention.
const SEVERITY: Record<string, number> = {
  expired: 4,
  unreachable: 3,
  expiring: 2,
  renewed: 1,
  ok: 0,
};
const TAG_COLORS = ['success', 'processing', 'warning', 'error', 'error'];

interface CertificateStatusTagProps {
  certificates: XrayCertificate[];
}

export default function CertificateStatusTag({ certificates }: CertificateStatusTagProps) {
  const { t } = useTranslation();
  const { datepicker } = useDatepicker();
  if (certificates.length === 0) return null;

  const worst = Math.max(...certificates.map((c) => SEVERITY[c.state] ?? 3));
  const date = (unix?: number) => (unix ? IntlUtil.formatDate(unix * 1000, datepicker) : '—');
  const lines = certificates.map((c) => {
    const state = t(STATE_KEYS[c.state] ?? 'pages.index.certStateUnreachable');
    const served = c.served
      ? t('pages.index.certServedUntil', { date: date(c.served.notAfter) })
      : c.probeError;
    const file =
      c.file && c.served && c.file.sha256 !== c.served.sha256
        ? ` · ${t('pages.index.certFileUntil', { date: date(c.file.notAfter) })}`
        : '';
    return (
      <div key={`${c.tag}-${c.port}-${c.transport}`}>
        {`${c.tag}:${c.port} (${c.transport}) — ${state}. ${served ?? ''}${file}`}
      </div>
    );
  });

  return (
    <Tooltip title={<span className="ov-error-detail">{lines}</span>}>
      <Tag color={TAG_COLORS[worst]} icon={<SafetyCertificateOutlined aria-hidden />}>
        {t('pages.index.certTitle')}
      </Tag>
    </Tooltip>
  );
}
