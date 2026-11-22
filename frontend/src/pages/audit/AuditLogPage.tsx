import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Col,
  ConfigProvider,
  Input,
  Layout,
  Row,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { DownloadOutlined, SafetyCertificateOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';

import { useTheme } from '@/hooks/useTheme';
import { useDatepicker } from '@/hooks/useDatepicker';
import {
  exportAuditLog,
  useAuditLogQuery,
  verifyAuditLog,
  type AuditFilter,
  type AuditLog,
  type AuditVerifyResult,
} from '@/api/queries/useAuditLog';
import DateTimePicker from '@/components/form/DateTimePicker';
import AppSidebar from '@/layouts/AppSidebar';
import { FileManager, IntlUtil } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

const PAGE_SIZE = 50;
// Mirrors service.AuditExportMaxRows: the server ends an export there.
const EXPORT_MAX_ROWS = 100000;

interface AuditFormValues {
  actor: string;
  action: string;
  outcome: string;
  from: number | null;
  to: number | null;
}

const EMPTY_FORM: AuditFormValues = { actor: '', action: '', outcome: '', from: null, to: null };

function auditFilter(values: AuditFormValues, page: number): AuditFilter {
  return {
    actor: values.actor.trim(),
    action: values.action.trim(),
    outcome: values.outcome,
    from: values.from ?? 0,
    to: values.to ?? 0,
    page,
    pageSize: PAGE_SIZE,
  };
}

export default function AuditLogPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { datepicker } = useDatepicker();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const [form, setForm] = useState<AuditFormValues>(EMPTY_FORM);
  const [applied, setApplied] = useState<AuditFormValues>(EMPTY_FORM);
  const [page, setPage] = useState(1);
  const [exporting, setExporting] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [verified, setVerified] = useState<AuditVerifyResult | null>(null);

  const filter = useMemo(() => auditFilter(applied, page), [applied, page]);
  const { data, isFetching, isError, error, refetch } = useAuditLogQuery(filter);

  const patch = useCallback((next: Partial<AuditFormValues>) => {
    setForm((prev) => ({ ...prev, ...next }));
  }, []);

  const search = useCallback(() => {
    // The same filter is the same query, which would not run again: Search always asks the server.
    if (page === 1 && JSON.stringify(form) === JSON.stringify(applied)) {
      void refetch();
      return;
    }
    setPage(1);
    setApplied({ ...form });
  }, [form, applied, page, refetch]);

  const reset = useCallback(() => {
    setPage(1);
    setForm(EMPTY_FORM);
    setApplied(EMPTY_FORM);
  }, []);

  const exportCsv = useCallback(async () => {
    setExporting(true);
    try {
      const csv = await exportAuditLog(auditFilter(applied, 1));
      FileManager.downloadTextFile(csv, 'audit-log.csv', { type: 'text/csv' });
    } catch (e) {
      messageApi.error((e as Error).message);
    } finally {
      setExporting(false);
    }
  }, [applied, messageApi]);

  const verify = useCallback(async () => {
    setVerifying(true);
    try {
      setVerified(await verifyAuditLog());
    } catch (e) {
      messageApi.error((e as Error).message);
    } finally {
      setVerifying(false);
    }
  }, [messageApi]);

  const columns = useMemo<ColumnsType<AuditLog>>(
    () => [
      { title: '#', dataIndex: 'id', width: 80 },
      {
        title: t('pages.audit.time'),
        dataIndex: 'time',
        width: 190,
        render: (time: number) => IntlUtil.formatDate(time, datepicker),
      },
      { title: t('pages.audit.actor'), dataIndex: 'actor' },
      { title: t('pages.audit.ip'), dataIndex: 'ip' },
      {
        title: t('pages.audit.action'),
        dataIndex: 'action',
        render: (action: string) => (
          <Typography.Text code style={{ wordBreak: 'break-all' }}>
            {action}
          </Typography.Text>
        ),
      },
      { title: t('pages.audit.target'), dataIndex: 'target' },
      {
        title: t('pages.audit.outcome'),
        dataIndex: 'outcome',
        render: (outcome: string, row) => (
          <Tag color={outcome === 'success' ? 'green' : 'red'} title={`${t('pages.audit.detail')}: ${row.detail}`}>
            {outcome === 'success' ? t('pages.audit.success') : t('pages.audit.failure')}
          </Tag>
        ),
      },
    ],
    [t, datepicker],
  );

  const pageClass = useMemo(() => {
    const classes = ['audit-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Row gutter={[16, 12]}>
              <Col span={24}>
                <Card size="small" title={t('pages.audit.title')}>
                  <Typography.Paragraph>{t('pages.audit.intro')}</Typography.Paragraph>
                  {verified && (
                    <Alert
                      type={verified.ok ? 'success' : 'error'}
                      showIcon
                      closable
                      onClose={() => setVerified(null)}
                      style={{ marginBottom: 12 }}
                      title={
                        verified.ok
                          ? t('pages.audit.verifyOk', { count: verified.checked })
                          : t('pages.audit.verifyBroken', {
                              id: verified.brokenId,
                              reason: verified.reason,
                            })
                      }
                    />
                  )}
                  <Row gutter={[8, 8]}>
                    <Col xs={24} md={12} lg={6}>
                      <Input
                        allowClear
                        value={form.actor}
                        placeholder={t('pages.audit.actorPlaceholder')}
                        aria-label={t('pages.audit.actor')}
                        onChange={(e) => patch({ actor: e.target.value })}
                        onPressEnter={search}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Input
                        allowClear
                        value={form.action}
                        placeholder={t('pages.audit.actionPlaceholder')}
                        aria-label={t('pages.audit.action')}
                        onChange={(e) => patch({ action: e.target.value })}
                        onPressEnter={search}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Select
                        style={{ width: '100%' }}
                        allowClear
                        value={form.outcome || undefined}
                        placeholder={t('pages.audit.outcome')}
                        aria-label={t('pages.audit.outcome')}
                        options={[
                          { value: 'success', label: t('pages.audit.success') },
                          { value: 'failure', label: t('pages.audit.failure') },
                        ]}
                        onChange={(value) => patch({ outcome: value ?? '' })}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Space.Compact style={{ width: '100%' }}>
                        <DateTimePicker
                          value={form.from == null ? null : dayjs(form.from)}
                          placeholder={t('from')}
                          aria-label={t('from')}
                          onChange={(next) => patch({ from: next ? next.valueOf() : null })}
                        />
                        <DateTimePicker
                          value={form.to == null ? null : dayjs(form.to)}
                          placeholder={t('to')}
                          aria-label={t('to')}
                          onChange={(next) => patch({ to: next ? next.valueOf() : null })}
                        />
                      </Space.Compact>
                    </Col>
                    <Col span={24}>
                      <Space wrap>
                        <Button
                          type="primary"
                          icon={<SearchOutlined aria-hidden />}
                          onClick={search}
                        >
                          {t('search')}
                        </Button>
                        <Button onClick={reset}>{t('clear')}</Button>
                        <Button
                          icon={<DownloadOutlined aria-hidden />}
                          loading={exporting}
                          onClick={() => void exportCsv()}
                        >
                          {t('pages.audit.exportCsv')}
                        </Button>
                        <Button
                          icon={<SafetyCertificateOutlined aria-hidden />}
                          loading={verifying}
                          onClick={() => void verify()}
                        >
                          {t('pages.audit.verify')}
                        </Button>
                      </Space>
                    </Col>
                  </Row>
                  <Typography.Paragraph type="secondary" style={{ margin: '8px 0 0' }}>
                    {t('pages.audit.exportHint', { count: EXPORT_MAX_ROWS })}
                  </Typography.Paragraph>
                </Card>
              </Col>
              <Col span={24}>
                <Card size="small">
                  {isError && (
                    <Alert
                      type="error"
                      showIcon
                      style={{ marginBottom: 12 }}
                      title={t('somethingWentWrong')}
                      description={(error as Error | null)?.message}
                    />
                  )}
                  <Table<AuditLog>
                    size="small"
                    rowKey="id"
                    columns={columns}
                    dataSource={data?.rows ?? []}
                    loading={isFetching}
                    scroll={{ x: 900 }}
                    locale={{ emptyText: t('pages.audit.empty') }}
                    pagination={{
                      current: page,
                      pageSize: PAGE_SIZE,
                      total: data?.total ?? 0,
                      showSizeChanger: false,
                      onChange: setPage,
                    }}
                  />
                </Card>
              </Col>
            </Row>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
