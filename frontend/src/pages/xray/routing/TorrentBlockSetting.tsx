import { useTranslation } from 'react-i18next';
import { Alert, Switch, Typography } from 'antd';

import { SettingListItem } from '@/components/ui';
import { torrentBlockNotice, useTorrentBlockQuery } from '@/api/queries/useTorrentBlockQuery';

// Applies on its own: the toggle is a panel setting, not part of the Xray template.
export default function TorrentBlockSetting() {
  const { t } = useTranslation();
  const { status, setEnabled, saving } = useTorrentBlockQuery();
  const notice = status ? torrentBlockNotice(status) : '';
  const c = status?.counters;

  return (
    <SettingListItem
      title={t('pages.xray.torrentBlock.title')}
      description={
        <>
          {t('pages.xray.torrentBlock.desc')}
          {notice && (
            <Alert
              type={notice === 'error' ? 'error' : 'warning'}
              showIcon
              className="mt-8"
              title={t(`pages.xray.torrentBlock.${notice}`, {
                error: status?.error ?? '',
              })}
            />
          )}
          {status?.enabled && status.active && c && (
            <div className="mt-8">
              <Typography.Text type="secondary">
                {t('pages.xray.torrentBlock.counters', {
                  caught: c.handshakes + c.dht + c.trackers + c.utp,
                  dropped: c.peerDrops + c.tunnelBans,
                })}
              </Typography.Text>
            </div>
          )}
        </>
      }
      paddings="small"
      control={
        <Switch
          aria-label={t('pages.xray.torrentBlock.title')}
          checked={!!status?.enabled}
          loading={saving}
          disabled={!status}
          onChange={(checked) => setEnabled(checked)}
        />
      }
    />
  );
}
