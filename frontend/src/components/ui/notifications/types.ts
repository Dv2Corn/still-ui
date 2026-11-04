import type { ReactNode } from 'react';

export interface NotificationEventConfig {
  key: string;
  label: string;
  settingKey: string;
  hint?: string;
  extra?: (props: {
    value: number;
    onChange: (v: number | null) => void;
    ariaLabel: string;
    /** Reads another numeric setting, for an event with more than one threshold. */
    settingValue: (key: string) => number;
    /** Writes another numeric setting. */
    setSetting: (key: string, v: number | null) => void;
  }) => ReactNode;
}

export interface NotificationGroupConfig {
  icon: ReactNode;
  title: string;
  events: NotificationEventConfig[];
}
