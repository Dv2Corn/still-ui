import { Input, InputNumber, Select } from 'antd';
import { useTranslation } from 'react-i18next';
import { AllSetting } from '@/models/setting';
import type { SubHwidNoIdPolicy } from '@/schemas/setting';
import { onNumber } from '@/utils/onNumber';
import { SettingListItem } from '@/components/ui';

interface DeviceLimitSettingsProps {
  allSetting: AllSetting;
  updateSetting: (patch: Partial<AllSetting>) => void;
}

const defaults = new AllSetting();

export default function DeviceLimitSettings({
  allSetting,
  updateSetting,
}: DeviceLimitSettingsProps) {
  const { t } = useTranslation();

  return (
    <>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.subHwidDefaultLimit')}
        description={t('pages.settings.subHwidDefaultLimitDesc')}
      >
        <InputNumber
          aria-label={t('pages.settings.subHwidDefaultLimit')}
          value={allSetting.subHwidDefaultLimit}
          min={0}
          max={1000}
          precision={0}
          style={{ width: '100%' }}
          onChange={onNumber((v) => updateSetting({ subHwidDefaultLimit: v }))}
        />
      </SettingListItem>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.subHwidNoIdPolicy')}
        description={t('pages.settings.subHwidNoIdPolicyDesc')}
      >
        <Select
          id="sub-hwid-no-id-policy"
          aria-label={t('pages.settings.subHwidNoIdPolicy')}
          value={allSetting.subHwidNoIdPolicy}
          onChange={(value: SubHwidNoIdPolicy) => updateSetting({ subHwidNoIdPolicy: value })}
          options={[
            { value: 'allow', label: t('pages.settings.subHwidNoIdAllow') },
            { value: 'refuse', label: t('pages.settings.subHwidNoIdRefuse') },
          ]}
        />
      </SettingListItem>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.subHwidExpireDays')}
        description={t('pages.settings.subHwidExpireDaysDesc')}
      >
        <InputNumber
          aria-label={t('pages.settings.subHwidExpireDays')}
          value={allSetting.subHwidExpireDays}
          min={0}
          max={3650}
          precision={0}
          style={{ width: '100%' }}
          onChange={onNumber((v) => updateSetting({ subHwidExpireDays: v }))}
        />
      </SettingListItem>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.subHwidLimitNotice')}
        description={t('pages.settings.subHwidLimitNoticeDesc')}
      >
        <Input
          aria-label={t('pages.settings.subHwidLimitNotice')}
          value={allSetting.subHwidLimitNotice}
          placeholder={defaults.subHwidLimitNotice}
          maxLength={256}
          allowClear
          onChange={(e) => updateSetting({ subHwidLimitNotice: e.target.value })}
        />
      </SettingListItem>
      <SettingListItem
        paddings="small"
        title={t('pages.settings.subHwidNoIdNotice')}
        description={t('pages.settings.subHwidNoIdNoticeDesc')}
      >
        <Input
          aria-label={t('pages.settings.subHwidNoIdNotice')}
          value={allSetting.subHwidNoIdNotice}
          placeholder={defaults.subHwidNoIdNotice}
          maxLength={256}
          allowClear
          onChange={(e) => updateSetting({ subHwidNoIdNotice: e.target.value })}
        />
      </SettingListItem>
    </>
  );
}
