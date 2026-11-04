import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Col,
  ConfigProvider,
  Empty,
  Layout,
  Modal,
  Result,
  Row,
  Spin,
  Typography,
  message,
} from 'antd';
import { PlusOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useInboundOptions } from '@/api/queries/useInboundOptions';
import {
  useReachProbeMutations,
  useReachProbesQuery,
  type ReachProbeInput,
  type ReachProbeView,
} from '@/api/queries/useReachProbes';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import ProbeCard from './ProbeCard';
import ProbeFormModal from './ProbeFormModal';
import ProbeTokenModal from './ProbeTokenModal';

export default function ProbesPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { data: probes, isFetching, isError, error, refetch } = useReachProbesQuery();
  const { data: inboundOptions = [] } = useInboundOptions();
  const { create, update, remove, rotateToken } = useReachProbeMutations();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<ReachProbeView | null>(null);
  const [token, setToken] = useState<string | null>(null);

  const onAdd = useCallback(() => {
    setEditing(null);
    setFormOpen(true);
  }, []);

  const onEdit = useCallback((probe: ReachProbeView) => {
    setEditing(probe);
    setFormOpen(true);
  }, []);

  const onSave = useCallback(
    async (input: ReachProbeInput) => {
      if (editing) return update(editing.id, input);
      const msg = await create(input);
      if (msg?.success && msg.obj?.token) setToken(msg.obj.token);
      return msg;
    },
    [editing, update, create],
  );

  const onRotate = useCallback(
    async (probe: ReachProbeView) => {
      const msg = await rotateToken(probe.id);
      if (msg?.success && msg.obj?.token) setToken(msg.obj.token);
    },
    [rotateToken],
  );

  const onDelete = useCallback(
    (probe: ReachProbeView) => {
      modal.confirm({
        title: t('pages.probes.deleteConfirm', { name: probe.name }),
        content: t('pages.probes.deleteHint'),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: () => remove(probe.id),
      });
    },
    [modal, remove, t],
  );

  const pageClass = useMemo(() => {
    const classes = ['probes-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  const fetched = probes !== undefined || isError;

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin aria-live="off" spinning={!fetched} delay={200} size="large">
              {!fetched ? (
                <div className="loading-spacer" />
              ) : isError ? (
                <Result
                  status="error"
                  title={t('somethingWentWrong')}
                  subTitle={(error as Error | null)?.message}
                  extra={
                    <Button type="primary" loading={isFetching} onClick={() => refetch()}>
                      {t('refresh')}
                    </Button>
                  }
                />
              ) : (
                <Row gutter={[isMobile ? 8 : 16, isMobile ? 8 : 12]}>
                  <Col span={24}>
                    <Card
                      size="small"
                      title={t('pages.probes.title')}
                      extra={
                        <Button type="primary" icon={<PlusOutlined aria-hidden />} onClick={onAdd}>
                          {t('pages.probes.addProbe')}
                        </Button>
                      }
                    >
                      <Typography.Paragraph>{t('pages.probes.intro')}</Typography.Paragraph>
                      <Alert type="info" showIcon message={t('pages.probes.alertsHint')} />
                    </Card>
                  </Col>
                  {(probes ?? []).length === 0 ? (
                    <Col span={24}>
                      <Card size="small">
                        <Empty description={t('pages.probes.empty')} />
                      </Card>
                    </Col>
                  ) : (
                    (probes ?? []).map((probe) => (
                      <Col span={24} key={probe.id}>
                        <ProbeCard
                          probe={probe}
                          onEdit={onEdit}
                          onDelete={onDelete}
                          onRotate={onRotate}
                        />
                      </Col>
                    ))
                  )}
                </Row>
              )}
            </Spin>
          </Layout.Content>
        </Layout>
        <ProbeFormModal
          open={formOpen}
          probe={editing}
          inboundOptions={inboundOptions}
          save={onSave}
          onOpenChange={setFormOpen}
        />
        <ProbeTokenModal token={token} onClose={() => setToken(null)} />
      </Layout>
    </ConfigProvider>
  );
}
