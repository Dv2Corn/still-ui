import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
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
import { DownloadOutlined, LeftOutlined, RightOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';

import { useTheme } from '@/hooks/useTheme';
import { useDatepicker } from '@/hooks/useDatepicker';
import { useInboundOptions } from '@/api/queries/useInboundOptions';
import { exportConnLog, useConnLogQuery, type ConnRecord } from '@/api/queries/useConnLog';
import DateTimePicker from '@/components/form/DateTimePicker';
import AppSidebar from '@/layouts/AppSidebar';
import { formatInboundLabel } from '@/lib/inbounds/label';
import {
  CONNLOG_EXPORT_MAX_ROWS,
  EMPTY_CONNLOG_FORM,
  connEndpoint,
  connLogFilter,
  type ConnLogFormValues,
} from '@/lib/connlog';
import { FileManager, IntlUtil, SizeFormatter } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

export default function ConnectionsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { datepicker } = useDatepicker();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  // A client's row on the Clients page links here with ?client=<email>.
  const [searchParams] = useSearchParams();
  const linkedClient = searchParams.get('client') ?? '';
  const [form, setForm] = useState<ConnLogFormValues>({
    ...EMPTY_CONNLOG_FORM,
    client: linkedClient,
  });
  const [applied, setApplied] = useState<ConnLogFormValues>(form);
  // The cursors of the pages opened so far; the last one is the page on screen.
  const [cursors, setCursors] = useState<string[]>([]);
  const [exporting, setExporting] = useState(false);

  const filter = useMemo(() => connLogFilter(applied, cursors.at(-1) ?? ''), [applied, cursors]);
  const { data: page, isFetching, isError, error, refetch } = useConnLogQuery(filter);
  const { data: inboundOptions = [] } = useInboundOptions();

  const patch = useCallback((next: Partial<ConnLogFormValues>) => {
    setForm((prev) => ({ ...prev, ...next }));
  }, []);

  const search = useCallback(() => {
    // The same filter is the same query, which would not run again: Search always asks the server.
    if (cursors.length === 0 && JSON.stringify(form) === JSON.stringify(applied)) {
      void refetch();
      return;
    }
    setCursors([]);
    setApplied({ ...form });
  }, [form, applied, cursors, refetch]);

  const reset = useCallback(() => {
    setCursors([]);
    setForm(EMPTY_CONNLOG_FORM);
    setApplied(EMPTY_CONNLOG_FORM);
  }, []);

  const exportCsv = useCallback(async () => {
    setExporting(true);
    try {
      const csv = await exportConnLog(connLogFilter(applied));
      FileManager.downloadTextFile(csv, 'connections.csv', { type: 'text/csv' });
    } catch (e) {
      messageApi.error((e as Error).message);
    } finally {
      setExporting(false);
    }
  }, [applied, messageApi]);

  const columns = useMemo<ColumnsType<ConnRecord>>(
    () => [
      {
        title: t('pages.connections.time'),
        dataIndex: 'time',
        width: 190,
        render: (time: number) => IntlUtil.formatDate(time, datepicker),
      },
      {
        title: t('pages.connections.client'),
        dataIndex: 'client',
        render: (client: string) =>
          client === '' ? (
            <Typography.Text type="secondary">—</Typography.Text>
          ) : (
            <Typography.Text copyable={{ text: client }}>{client}</Typography.Text>
          ),
      },
      {
        title: t('pages.connections.inbound'),
        dataIndex: 'inbound',
        render: (inbound: string, row) => (
          <Space size={4} wrap>
            <Tag>{inbound}</Tag>
            <RightOutlined aria-hidden />
            <Tag>{row.outbound}</Tag>
          </Space>
        ),
      },
      {
        title: t('pages.connections.source'),
        dataIndex: 'source',
        render: (source: string, row) => connEndpoint(source, row.sourcePort) || '—',
      },
      {
        title: t('pages.connections.destination'),
        dataIndex: 'host',
        render: (host: string, row) =>
          host === '' ? (
            <Typography.Text type="secondary">—</Typography.Text>
          ) : (
            <Space size={4} wrap>
              <Tag>{row.network}</Tag>
              <Typography.Text style={{ wordBreak: 'break-all' }}>
                {connEndpoint(host, row.port)}
              </Typography.Text>
            </Space>
          ),
      },
    ],
    [t, datepicker],
  );

  const pageClass = useMemo(() => {
    const classes = ['connections-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  // Two connections can agree on every column, so a row is keyed by its place on the page.
  const rows = useMemo(
    () => (page?.rows ?? []).map((row, index) => ({ ...row, key: index })),
    [page],
  );

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Row gutter={[16, 12]}>
              <Col span={24}>
                <Card size="small" title={t('pages.connections.title')}>
                  <Typography.Paragraph>{t('pages.connections.intro')}</Typography.Paragraph>
                  {page && !page.enabled && (
                    <Alert
                      type="warning"
                      showIcon
                      style={{ marginBottom: 12 }}
                      title={t('pages.connections.off')}
                      action={
                        <Button size="small" onClick={() => navigate('/settings#connections')}>
                          {t('pages.connections.openSettings')}
                        </Button>
                      }
                    />
                  )}
                  <Row gutter={[8, 8]}>
                    <Col xs={24} md={12} lg={6}>
                      <Input
                        allowClear
                        value={form.client}
                        placeholder={t('pages.connections.clientPlaceholder')}
                        aria-label={t('pages.connections.client')}
                        onChange={(e) => patch({ client: e.target.value })}
                        onPressEnter={search}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Select
                        style={{ width: '100%' }}
                        allowClear
                        showSearch={{ optionFilterProp: 'label' }}
                        value={form.inbound || undefined}
                        options={inboundOptions
                          .filter((inbound) => inbound.tag)
                          .map((inbound) => ({
                            value: inbound.tag as string,
                            label: formatInboundLabel(inbound.tag, inbound.remark),
                          }))}
                        placeholder={t('pages.connections.inbound')}
                        aria-label={t('pages.connections.inbound')}
                        onChange={(value) => patch({ inbound: value ?? '' })}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Input
                        allowClear
                        value={form.host}
                        placeholder={t('pages.connections.hostPlaceholder')}
                        aria-label={t('pages.connections.destination')}
                        onChange={(e) => patch({ host: e.target.value })}
                        onPressEnter={search}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <Input
                        allowClear
                        value={form.source}
                        placeholder={t('pages.connections.sourcePlaceholder')}
                        aria-label={t('pages.connections.source')}
                        onChange={(e) => patch({ source: e.target.value })}
                        onPressEnter={search}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <DateTimePicker
                        value={form.from == null ? null : dayjs(form.from)}
                        placeholder={t('from')}
                        aria-label={t('from')}
                        onChange={(next) => patch({ from: next ? next.valueOf() : null })}
                      />
                    </Col>
                    <Col xs={24} md={12} lg={6}>
                      <DateTimePicker
                        value={form.to == null ? null : dayjs(form.to)}
                        placeholder={t('to')}
                        aria-label={t('to')}
                        onChange={(next) => patch({ to: next ? next.valueOf() : null })}
                      />
                    </Col>
                    <Col xs={24} lg={12}>
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
                          {t('pages.connections.exportCsv')}
                        </Button>
                      </Space>
                    </Col>
                  </Row>
                  <Typography.Paragraph type="secondary" style={{ margin: '8px 0 0' }}>
                    {t('pages.connections.exportHint', { count: CONNLOG_EXPORT_MAX_ROWS })}
                    {page
                      ? ` ${t('pages.connections.onDisk')}: ${SizeFormatter.sizeFormat(page.sizeBytes)}`
                      : ''}
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
                  <Table<ConnRecord>
                    size="small"
                    rowKey="key"
                    columns={columns}
                    dataSource={rows}
                    loading={isFetching}
                    pagination={false}
                    scroll={{ x: 900 }}
                    locale={{ emptyText: t('pages.connections.empty') }}
                  />
                  <Space style={{ marginTop: 12 }}>
                    <Button
                      icon={<LeftOutlined aria-hidden />}
                      disabled={cursors.length === 0 || isFetching}
                      onClick={() => setCursors((prev) => prev.slice(0, -1))}
                    >
                      {t('pages.connections.newer')}
                    </Button>
                    <Button
                      disabled={!page?.next || isFetching}
                      onClick={() => page?.next && setCursors((prev) => [...prev, page.next])}
                    >
                      {t('pages.connections.older')}
                      <RightOutlined aria-hidden />
                    </Button>
                  </Space>
                </Card>
              </Col>
            </Row>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
