import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Input, InputNumber, Space, Switch, Tag, Typography } from 'antd';
import { CloudDownloadOutlined, LockOutlined } from '@ant-design/icons';
import { HttpUtil, SizeFormatter } from '@/utils';
import { onNumber } from '@/utils/onNumber';
import type { AllSetting } from '@/models/setting';
import { SettingListItem } from '@/components/ui';
import { NotifyTimeField } from './NotifyTimeField';

interface BackupTabProps {
  allSetting: AllSetting;
  updateSetting: (patch: Partial<AllSetting>) => void;
}

interface BackupResult {
  file?: string;
  size?: number;
  encrypted: boolean;
  time: number;
  error?: string;
  telegram?: string;
}

interface BackupEntry {
  name: string;
  size: number;
  time: string;
  encrypted: boolean;
}

interface BackupStatus {
  folder: string;
  last?: BackupResult | null;
  backups: BackupEntry[];
}

// Scheduled local backups (still-ui): settings, the last run and the files on disk.
export default function BackupTab({ allSetting, updateSetting }: BackupTabProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [running, setRunning] = useState(false);
  const { data: status } = useQuery({
    queryKey: ['localBackups'],
    queryFn: async () => {
      const res = await HttpUtil.get<BackupStatus>('/panel/api/server/localBackups', undefined, {
        silent: true,
      });
      return res?.success && res.obj ? res.obj : null;
    },
  });

  async function runNow() {
    setRunning(true);
    try {
      await HttpUtil.post('/panel/api/server/localBackup');
    } finally {
      setRunning(false);
      void queryClient.invalidateQueries({ queryKey: ['localBackups'] });
    }
  }

  const last = status?.last;

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.backupEnable')}
        description={t('pages.settings.backupEnableDesc')}
      >
        <Switch
          checked={allSetting.backupEnable}
          onChange={(v) => updateSetting({ backupEnable: v })}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.backupSchedule')}
        description={t('pages.settings.backupScheduleDesc')}
      >
        <NotifyTimeField
          value={allSetting.backupSchedule}
          onChange={(v) => updateSetting({ backupSchedule: v })}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.backupKeep')}
        description={t('pages.settings.backupKeepDesc')}
      >
        <InputNumber
          min={1}
          max={1000}
          value={allSetting.backupKeep}
          onChange={onNumber((v) => updateSetting({ backupKeep: v }))}
          style={{ width: '100%' }}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.backupRecipients')}
        description={t('pages.settings.backupRecipientsDesc')}
      >
        <Input.TextArea
          value={allSetting.backupAgeRecipients}
          onChange={(e) => updateSetting({ backupAgeRecipients: e.target.value })}
          autoSize={{ minRows: 2, maxRows: 6 }}
          placeholder="age1…"
          spellCheck={false}
          aria-label={t('pages.settings.backupRecipients')}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.backupTelegram')}
        description={t('pages.settings.backupTelegramDesc')}
      >
        <Switch
          checked={allSetting.backupTelegram}
          onChange={(v) => updateSetting({ backupTelegram: v })}
        />
      </SettingListItem>

      <Space orientation="vertical" size={12} style={{ width: '100%', marginTop: 16 }}>
        <Alert type="info" showIcon title={t('pages.settings.backupApplyHint')} />
        {last && (
          <Alert
            type={last.error ? 'error' : 'success'}
            showIcon
            title={
              last.error
                ? `${t('pages.settings.backupRunFailed')}: ${last.error}`
                : `${t('pages.settings.backupLast')}: ${last.file} (${SizeFormatter.sizeFormat(last.size)})`
            }
            description={
              <>
                {new Date(last.time * 1000).toLocaleString()}
                {last.telegram ? ` · Telegram: ${last.telegram}` : ''}
              </>
            }
          />
        )}
        <Button
          type="primary"
          icon={<CloudDownloadOutlined aria-hidden />}
          loading={running}
          onClick={() => void runNow()}
        >
          {t('pages.settings.backupRunNow')}
        </Button>
        <Typography.Text type="secondary">{t('pages.settings.backupRestoreHint')}</Typography.Text>
        <div>
          <Typography.Text strong>
            {t('pages.settings.backupList')}
            {status?.folder ? ` · ${status.folder}` : ''}
          </Typography.Text>
          {(status?.backups ?? []).length === 0 ? (
            <div>
              <Typography.Text type="secondary">
                {t('pages.settings.backupNoFiles')}
              </Typography.Text>
            </div>
          ) : (
            (status?.backups ?? []).map((b) => (
              <div key={b.name} style={{ padding: '4px 0' }}>
                <Space wrap size={8}>
                  <Typography.Text code copyable={{ text: b.name }}>
                    {b.name}
                  </Typography.Text>
                  <Typography.Text type="secondary">
                    {SizeFormatter.sizeFormat(b.size)}
                  </Typography.Text>
                  {b.encrypted && (
                    <Tag icon={<LockOutlined aria-hidden />} color="green">
                      {t('pages.settings.backupEncrypted')}
                    </Tag>
                  )}
                </Space>
              </div>
            ))
          )}
        </div>
      </Space>
    </>
  );
}
