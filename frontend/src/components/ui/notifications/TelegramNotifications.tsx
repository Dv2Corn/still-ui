import { InputNumber, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { onNumber } from '@/utils/onNumber';
import {
  CloudServerOutlined,
  HeartOutlined,
  ThunderboltOutlined,
  DesktopOutlined,
  DashboardOutlined,
  SafetyOutlined,
  RadarChartOutlined,
} from '@ant-design/icons';
import type { AllSetting } from '@/models/setting';
import { NotificationLayout } from './NotificationLayout';
import { NotificationGroup } from './NotificationGroup';
import type { NotificationGroupConfig } from './types';

const GROUPS: NotificationGroupConfig[] = [
  {
    icon: <CloudServerOutlined aria-hidden />,
    title: 'eventGroupOutbound',
    events: [
      {
        key: 'outbound.down',
        label: 'eventOutboundDown',
        settingKey: 'outboundDownThreshold',
        hint: 'eventDownThresholdHint',
        extra: ({ value, onChange, ariaLabel }) => (
          <InputNumber
            size="small"
            min={1}
            max={100}
            value={value}
            onChange={onChange}
            aria-label={ariaLabel}
            style={{ width: 80 }}
          />
        ),
      },
      { key: 'outbound.up', label: 'eventOutboundUp', settingKey: '' },
    ],
  },
  {
    icon: <ThunderboltOutlined aria-hidden />,
    title: 'eventGroupXray',
    events: [{ key: 'xray.crash', label: 'eventXrayCrash', settingKey: '' }],
  },
  {
    icon: <DesktopOutlined aria-hidden />,
    title: 'eventGroupNode',
    events: [
      {
        key: 'node.down',
        label: 'eventNodeDown',
        settingKey: 'nodeDownThreshold',
        hint: 'eventDownThresholdHint',
        extra: ({ value, onChange, ariaLabel }) => (
          <InputNumber
            size="small"
            min={1}
            max={100}
            value={value}
            onChange={onChange}
            aria-label={ariaLabel}
            style={{ width: 80 }}
          />
        ),
      },
      { key: 'node.up', label: 'eventNodeUp', settingKey: '' },
      {
        key: 'node.traffic',
        label: 'eventNodeTraffic',
        settingKey: '',
        hint: 'eventNodeTrafficHint',
      },
    ],
  },
  {
    icon: <DashboardOutlined aria-hidden />,
    title: 'eventGroupSystem',
    events: [
      {
        key: 'cpu.high',
        label: 'eventCPUHigh',
        settingKey: 'tgCpu',
        extra: ({ value, onChange, ariaLabel }) => (
          <InputNumber
            size="small"
            min={0}
            max={100}
            value={value}
            onChange={onChange}
            aria-label={ariaLabel}
            style={{ width: 80 }}
          />
        ),
      },
      {
        key: 'memory.high',
        label: 'eventMemoryHigh',
        settingKey: 'tgMemory',
        extra: ({ value, onChange, ariaLabel }) => (
          <InputNumber
            size="small"
            min={0}
            max={100}
            value={value}
            onChange={onChange}
            aria-label={ariaLabel}
            style={{ width: 80 }}
          />
        ),
      },
    ],
  },
  {
    icon: <SafetyOutlined aria-hidden />,
    title: 'eventGroupSecurity',
    events: [
      { key: 'login.attempt', label: 'eventLoginAttempt', settingKey: '' },
      // One subscription fetched from this many client networks within the hours next to it.
      {
        key: 'sub.leak',
        label: 'eventSubLeak',
        settingKey: 'subLeakNetworks',
        hint: 'eventSubLeakHint',
        extra: ({ value, onChange, ariaLabel, settingValue, setSetting }) => (
          <>
            <InputNumber
              size="small"
              min={2}
              max={64}
              value={value}
              onChange={onChange}
              aria-label={ariaLabel}
              style={{ width: 70 }}
            />
            <InputNumber
              size="small"
              min={1}
              max={168}
              value={settingValue('subLeakWindowHours')}
              onChange={onNumber((v) => setSetting('subLeakWindowHours', v))}
              aria-label={`${ariaLabel} (h)`}
              style={{ width: 80 }}
            />
          </>
        ),
      },
    ],
  },
  {
    icon: <RadarChartOutlined aria-hidden />,
    title: 'eventGroupProbe',
    events: [
      { key: 'probe.down', label: 'eventProbeDown', settingKey: '' },
      { key: 'probe.up', label: 'eventProbeUp', settingKey: '' },
      { key: 'probe.stale', label: 'eventProbeStale', settingKey: '' },
    ],
  },
];

function thresholdInput(min: number, max: number) {
  return function ThresholdInput({
    value,
    onChange,
    ariaLabel,
  }: {
    value: number;
    onChange: (v: number | null) => void;
    ariaLabel: string;
  }) {
    return (
      <InputNumber
        size="small"
        min={min}
        max={max}
        value={value}
        onChange={onChange}
        aria-label={ariaLabel}
        style={{ width: 80 }}
      />
    );
  };
}

// Server health alerts (still-ui): sent once when a problem starts, again every
// alertRemindHours while it lasts, and once when it is fixed.
const HEALTH_GROUP: NotificationGroupConfig = {
  icon: <HeartOutlined aria-hidden />,
  title: 'eventGroupHealth',
  events: [
    { key: 'xray.down', label: 'eventXrayDown', settingKey: '' },
    {
      key: 'xray.flapping',
      label: 'eventXrayFlapping',
      settingKey: 'alertXrayRestarts',
      hint: 'eventXrayFlappingHint',
      extra: ({ value, onChange, ariaLabel, settingValue, setSetting }) => (
        <>
          <InputNumber
            size="small"
            min={1}
            max={100}
            value={value}
            onChange={onChange}
            aria-label={ariaLabel}
            style={{ width: 70 }}
          />
          <InputNumber
            size="small"
            min={1}
            max={1440}
            value={settingValue('alertXrayRestartWindow')}
            onChange={onNumber((v) => setSetting('alertXrayRestartWindow', v))}
            aria-label={`${ariaLabel} (min)`}
            style={{ width: 80 }}
          />
        </>
      ),
    },
    {
      key: 'process.memory',
      label: 'eventProcessMemory',
      settingKey: 'alertMemory',
      hint: 'eventProcessMemoryHint',
      extra: thresholdInput(1, 100),
    },
    {
      key: 'disk.high',
      label: 'eventDiskHigh',
      settingKey: 'alertDisk',
      extra: thresholdInput(1, 100),
    },
    {
      key: 'cert.expiring',
      label: 'eventCertExpiring',
      settingKey: 'alertCertDays',
      hint: 'eventCertExpiringHint',
      extra: thresholdInput(1, 365),
    },
    { key: 'backup.failed', label: 'eventBackupFailed', settingKey: '' },
  ],
};

interface Props {
  allSetting: AllSetting;
  updateSetting: (patch: Partial<AllSetting>) => void;
}

export function TelegramNotifications({ allSetting, updateSetting }: Props) {
  const { t } = useTranslation();
  const events = allSetting.tgEnabledEvents || '';
  const selected = events
    ? events
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
    : [];

  function toggle(key: string) {
    const next = selected.includes(key) ? selected.filter((e) => e !== key) : [...selected, key];
    updateSetting({ tgEnabledEvents: next.join(',') });
  }

  function toggleAll(keys: string[]) {
    const allSelected = keys.every((v) => selected.includes(v));
    const next = allSelected
      ? selected.filter((v) => !keys.includes(v))
      : [...new Set([...selected, ...keys])];
    updateSetting({ tgEnabledEvents: next.join(',') });
  }

  return (
    <NotificationLayout>
      {GROUPS.map((group, i) => (
        <NotificationGroup
          key={i}
          config={group}
          selected={selected}
          onToggle={toggle}
          onToggleAll={toggleAll}
          allSetting={allSetting}
          updateSetting={updateSetting}
        />
      ))}
      <NotificationGroup
        config={HEALTH_GROUP}
        selected={selected}
        onToggle={toggle}
        onToggleAll={toggleAll}
        allSetting={allSetting}
        updateSetting={updateSetting}
      />
      <div
        style={{
          gridColumn: '1 / -1',
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
        }}
      >
        <Typography.Text>{t('pages.settings.alertRemindHours')}</Typography.Text>
        <InputNumber
          size="small"
          min={0}
          max={720}
          value={allSetting.alertRemindHours}
          onChange={onNumber((v) => updateSetting({ alertRemindHours: v }))}
          aria-label={t('pages.settings.alertRemindHours')}
          style={{ width: 80 }}
        />
        <Typography.Text type="secondary">
          {t('pages.settings.alertRemindHoursDesc')}
        </Typography.Text>
      </div>
    </NotificationLayout>
  );
}
