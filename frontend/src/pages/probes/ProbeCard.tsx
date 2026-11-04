import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Card, Popconfirm, Space, Table, Tag, Tooltip, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  DeleteOutlined,
  EditOutlined,
  HistoryOutlined,
  KeyOutlined,
  WarningOutlined,
} from '@ant-design/icons';

import { useReachProbeHistory, type ReachProbeView } from '@/api/queries/useReachProbes';
import type { ReachTargetView } from '@/generated/types';
import { IntlUtil } from '@/utils';
import { probeHistoryBuckets, type ProbeBucket } from '@/lib/probes';

const BUCKET_COLOR: Record<ProbeBucket, string> = {
  ok: 'var(--ant-color-success)',
  fail: 'var(--ant-color-error)',
  mixed: 'var(--ant-color-warning)',
  none: 'var(--ant-color-fill-secondary)',
};

function HistoryStrip({ probeId, targetKey }: { probeId: number; targetKey: string }) {
  const { t } = useTranslation();
  const { data = [], dataUpdatedAt } = useReachProbeHistory(probeId);
  const buckets = useMemo(
    () => probeHistoryBuckets(data, targetKey, dataUpdatedAt),
    [data, targetKey, dataUpdatedAt],
  );
  return (
    <div
      role="img"
      aria-label={t('pages.probes.history7d')}
      style={{ display: 'flex', gap: 1, height: 14, minWidth: 168 }}
    >
      {buckets.map((b, i) => (
        <span key={i} style={{ flex: 1, background: BUCKET_COLOR[b], borderRadius: 1 }} />
      ))}
    </div>
  );
}

interface Props {
  probe: ReachProbeView;
  onEdit: (probe: ReachProbeView) => void;
  onDelete: (probe: ReachProbeView) => void;
  onRotate: (probe: ReachProbeView) => void;
}

export default function ProbeCard({ probe, onEdit, onDelete, onRotate }: Props) {
  const { t } = useTranslation();
  const [showHistory, setShowHistory] = useState(false);

  const statusTag = (target: ReachTargetView) => {
    if (target.status === 'fail') return <Tag color="error">{t('pages.probes.status.fail')}</Tag>;
    if (target.status === 'ok' && target.lastOk)
      return <Tag color="success">{t('pages.probes.status.ok')}</Tag>;
    if (target.status === 'ok') return <Tag color="warning">{t('pages.probes.status.flaky')}</Tag>;
    return <Tag>{t('pages.probes.status.unknown')}</Tag>;
  };

  const columns: ColumnsType<ReachTargetView> = [
    { title: t('status'), key: 'status', width: 110, render: (_, r) => statusTag(r) },
    {
      title: t('pages.probes.fields.target'),
      key: 'label',
      render: (_, r) =>
        r.key === 'direct' ? (
          <Typography.Text>{t('pages.probes.directCheck')}</Typography.Text>
        ) : (
          <Tooltip title={r.key}>
            <Typography.Text>{r.label || r.key}</Typography.Text>
          </Tooltip>
        ),
    },
    {
      title: t('pages.probes.fields.lastCheck'),
      key: 'lastAt',
      render: (_, r) => (r.lastAt ? IntlUtil.formatDate(r.lastAt) : '—'),
    },
    {
      title: t('pages.probes.fields.latency'),
      key: 'latency',
      render: (_, r) =>
        r.lastOk ? (
          `${r.lastTtfbMs} / ${r.lastTotalMs} ms`
        ) : (
          <Typography.Text type="danger">{r.lastError || '—'}</Typography.Text>
        ),
    },
    {
      title: t('pages.probes.fields.uptime'),
      key: 'uptime',
      width: 110,
      render: (_, r) => (r.checks7d > 0 ? `${r.uptime7d}%` : '—'),
    },
    ...(showHistory
      ? [
          {
            title: t('pages.probes.history7d'),
            key: 'history',
            width: 200,
            render: (_: unknown, r: ReachTargetView) => (
              <HistoryStrip probeId={probe.id} targetKey={r.key} />
            ),
          },
        ]
      : []),
  ];

  const seen = probe.lastSeenAt
    ? t('pages.probes.lastReport', { time: IntlUtil.formatDate(probe.lastSeenAt) })
    : t('pages.probes.neverReported');

  return (
    <Card
      size="small"
      title={
        <Space wrap>
          <Typography.Text strong>{probe.name}</Typography.Text>
          {!probe.enabled && <Tag>{t('disabled')}</Tag>}
          {probe.stale && (
            <Tag icon={<WarningOutlined aria-hidden />} color="warning">
              {t('pages.probes.stale')}
            </Tag>
          )}
          <Typography.Text type="secondary">{seen}</Typography.Text>
          {probe.agentVersion && (
            <Typography.Text type="secondary">v{probe.agentVersion}</Typography.Text>
          )}
        </Space>
      }
      extra={
        <Space wrap>
          <Button
            size="small"
            icon={<HistoryOutlined aria-hidden />}
            onClick={() => setShowHistory((v) => !v)}
          >
            {t('pages.probes.history7d')}
          </Button>
          <Button size="small" icon={<EditOutlined aria-hidden />} onClick={() => onEdit(probe)}>
            {t('edit')}
          </Button>
          <Popconfirm title={t('pages.probes.rotateConfirm')} onConfirm={() => onRotate(probe)}>
            <Button size="small" icon={<KeyOutlined aria-hidden />}>
              {t('pages.probes.rotateToken')}
            </Button>
          </Popconfirm>
          <Button
            size="small"
            danger
            icon={<DeleteOutlined aria-hidden />}
            onClick={() => onDelete(probe)}
          >
            {t('delete')}
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
        {t('pages.probes.clientNote', { email: probe.clientEmail })}
      </Typography.Paragraph>
      <Table<ReachTargetView>
        size="small"
        rowKey="key"
        columns={columns}
        dataSource={probe.targets}
        pagination={false}
        scroll={{ x: true }}
        locale={{ emptyText: t('pages.probes.noResults') }}
      />
    </Card>
  );
}
