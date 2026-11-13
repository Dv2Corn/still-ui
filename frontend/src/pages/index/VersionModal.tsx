import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Checkbox, Collapse, Modal, Radio, Spin, Tag, Tooltip } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';

import { HttpUtil } from '@/utils';
import { activateOnKey } from '@/utils/a11y';
import type { Status } from '@/models/status';
import GeodataSection from './GeodataSection';
import './VersionModal.css';

interface BusyEvent {
  busy: boolean;
  tip?: string;
}

interface VersionModalProps {
  open: boolean;
  status: Status;
  onClose: () => void;
  onBusy: (e: BusyEvent) => void;
}

// The entry for still-ui's own Xray build. The panel cannot install it (only
// `still-ui update` does); every tag from getXrayVersion is an official release.
const STILL_BUILD = 'still-ui';

const GEOFILES = [
  'geosite.dat',
  'geoip.dat',
  'geosite_IR.dat',
  'geoip_IR.dat',
  'geosite_RU.dat',
  'geoip_RU.dat',
];

export default function VersionModal({ open, status, onClose, onBusy }: VersionModalProps) {
  const { t } = useTranslation();
  const [modal, modalContextHolder] = Modal.useModal();
  const [activeKey, setActiveKey] = useState<string | string[]>('1');
  const [versions, setVersions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [understood, setUnderstood] = useState(false);

  // State is set only in promise callbacks, so the open effect starts the
  // request without a synchronous setState.
  const fetchVersions = useCallback(
    () =>
      HttpUtil.get<string[]>('/panel/api/server/getXrayVersion')
        .then((msg) => {
          if (msg?.success) setVersions(msg.obj || []);
        })
        .finally(() => setLoading(false)),
    [],
  );

  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setLoading(true);
      setSelected(null);
      setUnderstood(false);
    }
  }

  useEffect(() => {
    if (open) void fetchVersions();
  }, [open, fetchVersions]);

  const stillBuild = !!status?.xray?.stillBuild;
  const installed = stillBuild ? STILL_BUILD : `v${status?.xray?.version}`;
  const current = selected ?? installed;
  const officialVersion = selected !== null && selected !== STILL_BUILD ? selected : null;

  function select(entry: string) {
    if (entry !== selected) setUnderstood(false);
    setSelected(entry);
  }

  async function installOfficial(version: string) {
    onClose();
    onBusy({ busy: true, tip: t('pages.index.dontRefresh') });
    try {
      await HttpUtil.post(`/panel/api/server/installXray/${version}`);
    } finally {
      onBusy({ busy: false });
    }
  }

  function updateGeofile(fileName: string) {
    const isSingle = !!fileName;
    modal.confirm({
      title: t('pages.index.geofileUpdateDialog'),
      content: isSingle
        ? t('pages.index.geofileUpdateDialogDesc').replace('#filename#', fileName)
        : t('pages.index.geofilesUpdateDialogDesc'),
      okText: t('confirm'),
      cancelText: t('cancel'),
      onOk: async () => {
        onClose();
        onBusy({ busy: true, tip: t('pages.index.dontRefresh') });
        const url = isSingle
          ? `/panel/api/server/updateGeofile/${fileName}`
          : '/panel/api/server/updateGeofile';
        try {
          await HttpUtil.post(url);
        } finally {
          onBusy({ busy: false });
        }
      },
    });
  }

  const activeKeyStr = Array.isArray(activeKey) ? activeKey[0] : activeKey;

  return (
    <Modal open={open} title={t('pages.index.xrayUpdates')} footer={null} onCancel={onClose}>
      {modalContextHolder}
      <Spin aria-live="off" spinning={loading}>
        <Collapse
          accordion
          activeKey={activeKey}
          onChange={setActiveKey}
          items={[
            {
              key: '1',
              label: 'Xray',
              children: (
                <>
                  <Alert
                    type="warning"
                    className="mb-12"
                    title={t('pages.index.xraySwitchClickDesk')}
                    showIcon
                  />
                  <div className="version-list" data-testid="xray-still-build">
                    <div className="version-list-item">
                      <div className="version-still">
                        <div>
                          <Tag color="gold">{t('pages.index.xrayStillBuildTitle')}</Tag>
                          {stillBuild && status?.xray?.version && (
                            <Tag>{`v${status.xray.version}`}</Tag>
                          )}
                          {stillBuild && <Tag color="blue">{t('pages.index.xrayInstalled')}</Tag>}
                        </div>
                        <div className="version-still-desc">
                          {t('pages.index.xrayStillBuildDesc')}
                        </div>
                      </div>
                      <Radio
                        checked={current === STILL_BUILD}
                        aria-label={t('pages.index.xrayStillBuildTitle')}
                        onClick={() => select(STILL_BUILD)}
                      />
                    </div>
                  </div>
                  {selected === STILL_BUILD && !stillBuild && (
                    <Alert
                      type="info"
                      className="mt-12"
                      title={t('pages.index.xrayStillBuildBack')}
                      showIcon
                    />
                  )}
                  <div className="version-section-title">
                    {t('pages.index.xrayOfficialReleases')}
                  </div>
                  <div className="version-list" data-testid="xray-official-list">
                    {versions.map((version, index) => (
                      <div key={version} className="version-list-item">
                        <div>
                          <Tag color={index % 2 === 0 ? 'purple' : 'green'}>{version}</Tag>
                          {!stillBuild && version === installed && (
                            <Tag color="blue">{t('pages.index.xrayInstalled')}</Tag>
                          )}
                        </div>
                        <Radio
                          checked={version === current}
                          aria-label={version}
                          onClick={() => select(version)}
                        />
                      </div>
                    ))}
                  </div>
                  {officialVersion && (
                    <div className="official-warning" data-testid="xray-official-warning">
                      <Alert
                        type="warning"
                        showIcon
                        title={t('pages.index.xrayOfficialWarnTitle').replace(
                          '#version#',
                          officialVersion,
                        )}
                        description={t('pages.index.xrayOfficialWarnDesc')}
                      />
                      <Checkbox
                        className="mt-12"
                        checked={understood}
                        onChange={(e) => setUnderstood(e.target.checked)}
                      >
                        {t('pages.index.xrayOfficialConfirm')}
                      </Checkbox>
                      <div className="actions-row">
                        <Button
                          type="primary"
                          danger
                          disabled={!understood}
                          onClick={() => void installOfficial(officialVersion)}
                        >
                          {t('pages.index.xrayInstallVersion').replace(
                            '#version#',
                            officialVersion,
                          )}
                        </Button>
                      </div>
                    </div>
                  )}
                </>
              ),
            },
            {
              key: '2',
              label: 'Geofiles',
              children: (
                <>
                  <div className="version-list">
                    {GEOFILES.map((file, index) => (
                      <div key={file} className="version-list-item">
                        <Tag color={index % 2 === 0 ? 'purple' : 'green'}>{file}</Tag>
                        <Tooltip title={t('update')}>
                          <ReloadOutlined
                            className="reload-icon"
                            role="button"
                            tabIndex={0}
                            aria-label={t('update')}
                            onClick={() => updateGeofile(file)}
                            onKeyDown={activateOnKey(() => updateGeofile(file))}
                          />
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                  <div className="actions-row">
                    <Button onClick={() => updateGeofile('')}>
                      {t('pages.index.geofilesUpdateAll')}
                    </Button>
                  </div>
                </>
              ),
            },
            {
              key: '3',
              label: t('pages.index.geodataTitle'),
              children: (
                <GeodataSection active={activeKeyStr === '3'} onBusy={onBusy} onClose={onClose} />
              ),
            },
          ]}
        />
      </Spin>
    </Modal>
  );
}
