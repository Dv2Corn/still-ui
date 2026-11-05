import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Checkbox, Radio, Select, Space, Typography } from 'antd';

import { selectTagRender } from '@/components/form';
import { SettingListItem } from '@/components/ui';
import { useNodesQuery } from '@/api/queries/useNodesQuery';
import {
  egressHostNotice,
  splitCustomEntries,
  useEgressFilterQuery,
  type EgressFilterInput,
  type EgressFilterNodeState,
  type EgressFilterStatus,
} from '@/api/queries/useEgressFilterQuery';

function formFrom(status: EgressFilterStatus): EgressFilterInput {
  const s = status.settings;
  return {
    smtp: !!s.smtp,
    mailSubmission: !!s.mailSubmission,
    private: !!s.private,
    server: !!s.server,
    custom: s.custom ?? [],
    nodeScope: s.nodeScope ?? '',
    nodeIds: s.nodeIds ?? [],
  };
}

// Applies on its own, like "Block torrents for clients": a panel setting, not part of
// the Xray template.
export default function EgressFilterSetting() {
  const { t } = useTranslation();
  const { status, save, saving } = useEgressFilterQuery();
  const { nodes } = useNodesQuery();
  // Unsaved edits; null shows the stored filter as it is.
  const [draft, setDraft] = useState<EgressFilterInput | null>(null);
  const stored = useMemo(() => (status ? formFrom(status) : null), [status]);
  const form = draft ?? stored;
  const dirty = !!draft && JSON.stringify(draft) !== JSON.stringify(stored);
  const nodeOptions = useMemo(
    () => nodes.map((n) => ({ label: n.name || n.remark || `#${n.id}`, value: n.id })),
    [nodes],
  );

  const set = (patch: Partial<EgressFilterInput>) => {
    if (form) setDraft({ ...form, ...patch });
  };
  const apply = async () => {
    if (!form) return;
    const msg = await save(form);
    if (msg?.success) setDraft(null);
  };
  const notice = status ? egressHostNotice(status) : '';
  const c = status?.host.counters;
  const ports = (status?.serverPorts ?? []).join(', ');

  const nodeLine = (n: EgressFilterNodeState) => {
    if (n.error) return t('pages.xray.egressFilter.nodeError', { name: n.name, error: n.error });
    if (!n.synced) return t('pages.xray.egressFilter.nodePending', { name: n.name });
    return n.wanted === 'release'
      ? t('pages.xray.egressFilter.nodeReleased', { name: n.name })
      : t('pages.xray.egressFilter.nodeApplied', { name: n.name });
  };

  return (
    <SettingListItem
      title={t('pages.xray.egressFilter.title')}
      description={
        <>
          {t('pages.xray.egressFilter.desc')}
          {status?.settings.managedBy && (
            <Alert
              type="info"
              showIcon
              className="mt-8"
              title={t('pages.xray.egressFilter.managed')}
            />
          )}
          {notice && (
            <Alert
              type={notice === 'error' ? 'error' : 'warning'}
              showIcon
              className="mt-8"
              title={t(`pages.xray.egressFilter.${notice}`, { error: status?.error ?? '' })}
            />
          )}
          {status?.hostNeeded && status.host.active && c && (
            <div className="mt-8">
              <Typography.Text type="secondary">
                {t('pages.xray.egressFilter.counters', {
                  mail: c.mail,
                  private: c.private,
                  server: c.server,
                  custom: c.custom,
                })}
              </Typography.Text>
            </div>
          )}
        </>
      }
      paddings="small"
    >
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Checkbox
          checked={!!form?.smtp}
          disabled={!form}
          onChange={(e) => set({ smtp: e.target.checked })}
        >
          {t('pages.xray.egressFilter.smtp')}
        </Checkbox>
        <Checkbox
          checked={!!form?.mailSubmission}
          disabled={!form}
          onChange={(e) => set({ mailSubmission: e.target.checked })}
        >
          {t('pages.xray.egressFilter.submission')}
        </Checkbox>
        <Checkbox
          checked={!!form?.private}
          disabled={!form}
          onChange={(e) => set({ private: e.target.checked })}
        >
          {t('pages.xray.egressFilter.private')}
        </Checkbox>
        {form?.private && status?.templatePrivateBlocked && (
          <Typography.Text type="secondary">
            {t('pages.xray.egressFilter.privateInTemplate')}
          </Typography.Text>
        )}
        <Checkbox
          checked={!!form?.server}
          disabled={!form}
          onChange={(e) => set({ server: e.target.checked })}
        >
          {t('pages.xray.egressFilter.server')}
        </Checkbox>
        {form?.server && (
          <Typography.Text type="secondary">
            {t('pages.xray.egressFilter.serverPorts', { ports })}{' '}
            {t('pages.xray.egressFilter.serverAddrs')}
          </Typography.Text>
        )}
        <Select
          mode="tags"
          aria-label={t('pages.xray.egressFilter.custom')}
          placeholder={t('pages.xray.egressFilter.custom')}
          tagRender={selectTagRender}
          value={form?.custom ?? []}
          disabled={!form}
          open={false}
          tokenSeparators={[',', ' ', ';']}
          style={{ width: '100%' }}
          onChange={(v: string[]) => set({ custom: splitCustomEntries(v) })}
        />
        <Typography.Text type="secondary">
          {t('pages.xray.egressFilter.customHint')}
        </Typography.Text>
        {(nodeOptions.length > 0 || (form?.nodeScope ?? '') !== '') && (
          <>
            <Radio.Group
              aria-label={t('pages.xray.egressFilter.nodes')}
              value={form?.nodeScope ?? ''}
              disabled={!form}
              onChange={(e) => set({ nodeScope: e.target.value })}
              options={[
                { label: t('pages.xray.egressFilter.nodesNone'), value: '' },
                { label: t('pages.xray.egressFilter.nodesAll'), value: 'all' },
                { label: t('pages.xray.egressFilter.nodesSelected'), value: 'selected' },
              ]}
            />
            {form?.nodeScope === 'selected' && (
              <Select
                mode="multiple"
                tagRender={selectTagRender}
                aria-label={t('pages.xray.egressFilter.nodes')}
                value={form.nodeIds ?? []}
                options={nodeOptions}
                style={{ width: '100%' }}
                onChange={(v: number[]) => set({ nodeIds: v })}
              />
            )}
            {(status?.nodes ?? []).map((n) => (
              <Typography.Text key={n.id} type={n.error ? 'danger' : 'secondary'}>
                {nodeLine(n)}
              </Typography.Text>
            ))}
          </>
        )}
        <Button
          type="primary"
          loading={saving}
          disabled={!form || !dirty}
          onClick={() => void apply()}
        >
          {t('pages.xray.egressFilter.apply')}
        </Button>
      </Space>
    </SettingListItem>
  );
}
