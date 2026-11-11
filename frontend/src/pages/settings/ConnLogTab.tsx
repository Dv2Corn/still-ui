import { useTranslation } from 'react-i18next';
import { Alert, InputNumber, Select, Switch } from 'antd';

import { onNumber } from '@/utils/onNumber';
import type { AllSetting } from '@/models/setting';
import { SettingListItem } from '@/components/ui';

interface ConnLogTabProps {
  allSetting: AllSetting;
  updateSetting: (patch: Partial<AllSetting>) => void;
}

// Connection log (still-ui): the switch, how long and how much is kept, and what is stored.
export default function ConnLogTab({ allSetting, updateSetting }: ConnLogTabProps) {
  const { t } = useTranslation();

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.connLogEnable')}
        description={t('pages.settings.connLogEnableDesc')}
      >
        <Switch
          checked={allSetting.connLogEnable}
          onChange={(v) => updateSetting({ connLogEnable: v })}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.connLogRetention')}
        description={t('pages.settings.connLogRetentionDesc')}
      >
        <InputNumber
          min={1}
          max={3650}
          value={allSetting.connLogRetentionDays}
          onChange={onNumber((v) => updateSetting({ connLogRetentionDays: v }))}
          style={{ width: '100%' }}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.connLogMaxSize')}
        description={t('pages.settings.connLogMaxSizeDesc')}
      >
        <InputNumber
          min={16}
          max={1048576}
          value={allSetting.connLogMaxSizeMB}
          onChange={onNumber((v) => updateSetting({ connLogMaxSizeMB: v }))}
          style={{ width: '100%' }}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.connLogDestination')}
        description={t('pages.settings.connLogDestinationDesc')}
      >
        <Select
          value={allSetting.connLogDestination}
          onChange={(v) => updateSetting({ connLogDestination: v })}
          style={{ width: '100%' }}
          aria-label={t('pages.settings.connLogDestination')}
          options={[
            { value: 'full', label: t('pages.settings.connLogDestinationFull') },
            { value: 'domain', label: t('pages.settings.connLogDestinationDomain') },
            { value: 'none', label: t('pages.settings.connLogStoreNothing') },
          ]}
        />
      </SettingListItem>

      <SettingListItem
        paddings="small"
        title={t('pages.settings.connLogSource')}
        description={t('pages.settings.connLogSourceDesc')}
      >
        <Select
          value={allSetting.connLogSource}
          onChange={(v) => updateSetting({ connLogSource: v })}
          style={{ width: '100%' }}
          aria-label={t('pages.settings.connLogSource')}
          options={[
            { value: 'full', label: t('pages.settings.connLogSourceFull') },
            { value: 'masked', label: t('pages.settings.connLogSourceMasked') },
            { value: 'none', label: t('pages.settings.connLogStoreNothing') },
          ]}
        />
      </SettingListItem>

      <Alert
        type="info"
        showIcon
        style={{ marginTop: 16 }}
        title={t('pages.settings.connLogPrivacyHint')}
      />
    </>
  );
}
