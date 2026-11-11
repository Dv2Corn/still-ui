import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, Checkbox, Radio, Space, Tag, Typography, Upload } from 'antd';
import { UploadOutlined } from '@ant-design/icons';
import { HttpUtil } from '@/utils';
import type { PanelImportReport } from '@/generated/types';
import { SettingListItem } from '@/components/ui';

type ImportSource = 'marzban' | 'x-ui';

// Import from Marzban or an old x-ui (still-ui): upload, read the dry-run report, confirm.
export default function ImportTab() {
  const { t } = useTranslation();
  const [source, setSource] = useState<ImportSource>('marzban');
  const [db, setDb] = useState<File | null>(null);
  const [config, setConfig] = useState<File | null>(null);
  const [merge, setMerge] = useState(false);
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<PanelImportReport | null>(null);

  const ready = db !== null && (source === 'x-ui' || config !== null);

  async function run(apply: boolean) {
    if (!db) return;
    const form = new FormData();
    form.append('source', source);
    form.append('db', db);
    if (source === 'marzban' && config) form.append('config', config);
    form.append('apply', String(apply));
    form.append('merge', String(merge));
    setBusy(true);
    try {
      const res = await HttpUtil.post<PanelImportReport>('/panel/api/server/importPanel', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      });
      setReport(res?.success && res.obj ? res.obj : null);
    } finally {
      setBusy(false);
    }
  }

  function pick(set: (file: File | null) => void) {
    return {
      maxCount: 1,
      beforeUpload: (file: File) => {
        set(file);
        setReport(null);
        return false;
      },
      onRemove: () => {
        set(null);
        setReport(null);
      },
    };
  }

  const importable = report?.inbounds.some((inbound) => !inbound.skipped) ?? false;

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.importSource')}
        description={t('pages.settings.importIntro')}
      >
        <Radio.Group
          value={source}
          onChange={(e) => {
            setSource(e.target.value as ImportSource);
            setReport(null);
          }}
          options={[
            { value: 'marzban', label: 'Marzban' },
            { value: 'x-ui', label: t('pages.settings.importSourceXui') },
          ]}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.importDb')}
        description={t('pages.settings.importDbDesc')}
      >
        <Upload {...pick(setDb)}>
          <Button icon={<UploadOutlined aria-hidden />}>{t('pages.settings.importDb')}</Button>
        </Upload>
      </SettingListItem>

      {source === 'marzban' && (
        <SettingListItem
          paddings="small"
          title={t('pages.settings.importConfig')}
          description={t('pages.settings.importConfigDesc')}
        >
          <Upload accept=".json" {...pick(setConfig)}>
            <Button icon={<UploadOutlined aria-hidden />}>
              {t('pages.settings.importConfig')}
            </Button>
          </Upload>
        </SettingListItem>
      )}

      <div style={{ padding: '10px 20px' }}>
        <Space orientation="vertical" style={{ width: '100%' }}>
          <Button type="primary" disabled={!ready} loading={busy} onClick={() => run(false)}>
            {t('pages.settings.importCheck')}
          </Button>

          {report && (
            <>
              {report.applied && (
                <Alert
                  type="success"
                  showIcon
                  title={t('pages.settings.importDone', { file: report.backup })}
                />
              )}
              <Card size="small" title={t('pages.settings.importReport')}>
                <Space orientation="vertical" style={{ width: '100%' }}>
                  {report.inbounds.map((inbound) => (
                    <Space wrap key={inbound.tag}>
                      <Tag>{inbound.protocol}</Tag>
                      <Typography.Text strong>{inbound.tag}</Typography.Text>
                      <Typography.Text type="secondary">:{inbound.port}</Typography.Text>
                      {inbound.skipped ? (
                        <Typography.Text type="danger">
                          {t('pages.settings.importSkipped')}: {inbound.skipped}
                        </Typography.Text>
                      ) : (
                        <Typography.Text>
                          {t('pages.settings.importClients', { count: inbound.clients })}
                        </Typography.Text>
                      )}
                    </Space>
                  ))}
                </Space>
              </Card>
              <Typography.Text>
                {t('pages.settings.importClients', { count: report.clients })}
                {' · '}
                {t('pages.settings.importLegacyLinks', { count: report.legacySubLinks })}
              </Typography.Text>
              {report.admins.length > 0 && (
                <Typography.Text type="secondary">
                  {t('pages.settings.importAdmins', { list: report.admins.join(', ') })}
                </Typography.Text>
              )}
              {report.notes.length > 0 && (
                <Card size="small" title={t('pages.settings.importNotes')}>
                  <Space orientation="vertical">
                    {report.notes.map((note) => (
                      <Typography.Text key={note}>{note}</Typography.Text>
                    ))}
                  </Space>
                </Card>
              )}
              {!report.applied && report.notEmpty && (
                <Alert
                  type="warning"
                  showIcon
                  title={t('pages.settings.importNotEmpty')}
                  description={
                    <Checkbox checked={merge} onChange={(e) => setMerge(e.target.checked)}>
                      {t('pages.settings.importMerge')}
                    </Checkbox>
                  }
                />
              )}
              {!report.applied && (
                <Button
                  danger
                  type="primary"
                  disabled={!importable || (report.notEmpty && !merge)}
                  loading={busy}
                  onClick={() => run(true)}
                >
                  {t('pages.settings.importApply')}
                </Button>
              )}
            </>
          )}
        </Space>
      </div>
    </>
  );
}
