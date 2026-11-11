import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Checkbox, Input, Modal, Space, Table, Tag, Tooltip, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';

import type { RealityScanResult } from '@/generated/types';

// xray-core ML-DSA-65 REALITY min peer cert-chain size (not defined in this repo).
export const MLDSA65_MIN_CERT_CHAIN_BYTES = 3500;

interface RealityTargetScannerModalProps {
  open: boolean;
  onClose: () => void;
  scanRealityCandidates: (targets?: string, neighbours?: boolean) => Promise<RealityScanResult[]>;
  onPick: (result: RealityScanResult) => void;
  mldsa65Enabled?: boolean;
}

export default function RealityTargetScannerModal({
  open,
  onClose,
  scanRealityCandidates,
  onPick,
  mldsa65Enabled = false,
}: RealityTargetScannerModalProps) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState('');
  const [neighbours, setNeighbours] = useState(false);
  const [results, setResults] = useState<RealityScanResult[]>([]);
  const scanRef = useRef(scanRealityCandidates);
  useEffect(() => {
    scanRef.current = scanRealityCandidates;
  });

  const applyScan = useCallback(async (targets?: string, withNeighbours?: boolean) => {
    try {
      setResults(await scanRef.current(targets, withNeighbours));
    } finally {
      setLoading(false);
    }
  }, []);

  const runScan = useCallback(
    (targets?: string) => {
      setLoading(true);
      setResults([]);
      void applyScan(targets, neighbours);
    },
    [applyScan, neighbours],
  );

  // Clearing the previous results is done during render so the auto-scan effect
  // carries only the request itself.
  const [scannedOpen, setScannedOpen] = useState(false);
  if (open !== scannedOpen) {
    setScannedOpen(open);
    if (open) {
      setResults([]);
      setLoading(true);
      setNeighbours(false);
    }
  }

  useEffect(() => {
    if (open) void applyScan();
  }, [open, applyScan]);

  // A probe that never reached TLS has nothing to mark as failed.
  const checkTag = (value: string, passed: boolean) =>
    value ? <Tag color={passed ? 'success' : 'error'}>{value}</Tag> : '—';

  const columns: ColumnsType<RealityScanResult> = [
    {
      title: t('pages.inbounds.form.target'),
      dataIndex: 'target',
      key: 'target',
      width: 200,
      render: (target: string, row) => (
        <Tooltip title={row.ip ? `${target} — ${row.ip}` : target}>
          <div style={{ lineHeight: 1.25 }}>
            <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {target}
            </div>
            {row.ip ? <div style={{ color: '#999', fontSize: 12 }}>{row.ip}</div> : null}
          </div>
        </Tooltip>
      ),
    },
    {
      title: t('pages.inbounds.form.scanStatus'),
      dataIndex: 'feasible',
      key: 'feasible',
      width: 95,
      render: (feasible: boolean, row) =>
        feasible ? (
          <Tag color="success">{t('pages.inbounds.form.scanFeasible')}</Tag>
        ) : (
          <Tooltip title={row.reason}>
            <Tag color="warning">{t('pages.inbounds.form.scanNotFeasible')}</Tag>
          </Tooltip>
        ),
    },
    {
      title: 'TLS',
      dataIndex: 'tlsVersion',
      key: 'tlsVersion',
      width: 60,
      render: (v: string, row) => checkTag(v, row.tls13),
    },
    {
      title: 'ALPN',
      dataIndex: 'alpn',
      key: 'alpn',
      width: 90,
      render: (v: string, row) => checkTag(v, row.h2),
    },
    {
      title: t('pages.inbounds.form.scanCurve'),
      dataIndex: 'curveID',
      key: 'curveID',
      width: 150,
      render: (v: string, row) => (
        <>
          {checkTag(v, row.x25519)}
          {row.mlkem768 ? <Tag>ML-KEM</Tag> : null}
        </>
      ),
    },
    {
      title: t('pages.inbounds.form.scanCert'),
      dataIndex: 'certSubject',
      key: 'certSubject',
      width: 160,
      ellipsis: true,
      render: (_: string, row) =>
        !row.certValid ? (
          <Tag color={row.tlsVersion ? 'error' : undefined}>
            {t('pages.inbounds.form.scanCertInvalid')}
          </Tag>
        ) : row.certExpiresSoon ? (
          <Tooltip title={`${row.certSubject} (${row.certIssuer})`}>
            <Tag color="warning">
              {t('pages.inbounds.form.scanCertExpiresSoon', { days: row.certDaysLeft })}
            </Tag>
          </Tooltip>
        ) : (
          <Tooltip title={`${row.certSubject} (${row.certIssuer})`}>
            <span>{row.certSubject || '—'}</span>
          </Tooltip>
        ),
    },
    {
      title: t('pages.inbounds.form.scanCertChain'),
      dataIndex: 'certChainBytes',
      key: 'certChainBytes',
      width: 100,
      render: (bytes: number) => {
        if (!bytes) return '—';
        if (mldsa65Enabled && bytes < MLDSA65_MIN_CERT_CHAIN_BYTES) {
          return (
            <Tooltip
              title={t('pages.inbounds.form.scanMldsaCertChainTooSmall', {
                length: bytes,
                min: MLDSA65_MIN_CERT_CHAIN_BYTES,
              })}
            >
              <Tag color="warning">{bytes} B</Tag>
            </Tooltip>
          );
        }
        return `${bytes} B`;
      },
    },
    {
      title: t('pages.inbounds.form.scanLatency'),
      dataIndex: 'latencyMs',
      key: 'latencyMs',
      width: 85,
      render: (v: number) => (v > 0 ? `${v} ms` : '—'),
    },
    {
      title: '',
      key: 'action',
      width: 64,
      render: (_, row) => (
        <Button
          type="link"
          size="small"
          onClick={() => {
            onPick(row);
            onClose();
          }}
        >
          {t('pages.inbounds.form.scanUse')}
        </Button>
      ),
    },
  ];

  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={[
        <Button key="rescan" onClick={() => runScan(query.trim() || undefined)} loading={loading}>
          {t('pages.inbounds.form.scanRescan')}
        </Button>,
        <Button key="close" type="primary" onClick={onClose}>
          {t('close')}
        </Button>,
      ]}
      title={t('pages.inbounds.form.scanModalTitle')}
      width={1120}
    >
      <Space orientation="vertical" size="small" style={{ width: '100%' }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {t('pages.inbounds.form.scanModalDesc')} {t('pages.inbounds.form.scanModalChoice')}
        </Typography.Paragraph>
        <Input.Search
          aria-label={t('pages.inbounds.form.scanDiscoverPlaceholder')}
          allowClear
          enterButton={t('pages.inbounds.form.scan')}
          loading={loading}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onSearch={() => runScan(query.trim() || undefined)}
          placeholder={t('pages.inbounds.form.scanDiscoverPlaceholder')}
        />
        <Checkbox checked={neighbours} onChange={(e) => setNeighbours(e.target.checked)}>
          {t('pages.inbounds.form.scanNeighbours')}
        </Checkbox>
        {neighbours ? (
          <Typography.Text type="warning">
            {t('pages.inbounds.form.scanNeighboursHint')}
          </Typography.Text>
        ) : null}
        <Table<RealityScanResult>
          size="small"
          rowKey="target"
          loading={loading}
          columns={columns}
          dataSource={results}
          pagination={false}
          scroll={{ y: 360 }}
        />
      </Space>
    </Modal>
  );
}
