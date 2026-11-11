import { useTranslation } from 'react-i18next';
import { Alert, Button, Space } from 'antd';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';

interface SubLocalDirectProps {
  country: string;
  shadowrocketConfUrl: string;
  lang: string;
  onCopy: (value: string) => void;
}

export default function SubLocalDirect({
  country,
  shadowrocketConfUrl,
  lang,
  onCopy,
}: SubLocalDirectProps) {
  const { t } = useTranslation();
  const code = country.toUpperCase();
  const name = new Intl.DisplayNames([lang], { type: 'region' }).of(code) || code;

  return (
    <Alert
      type="info"
      showIcon
      className="sub-announce"
      title={t('subscription.localDirectTitle', { country: name })}
      description={
        <>
          <div>{t('subscription.localDirectDesc')}</div>
          {shadowrocketConfUrl && (
            <>
              <Space wrap style={{ margin: '8px 0 4px' }}>
                <Button
                  size="small"
                  href={shadowrocketConfUrl}
                  icon={<DownloadOutlined aria-hidden />}
                >
                  {t('subscription.localDirectShadowrocket')}
                </Button>
                <Button
                  size="small"
                  icon={<CopyOutlined aria-hidden />}
                  onClick={() => onCopy(shadowrocketConfUrl)}
                >
                  {t('copy')}
                </Button>
              </Space>
              <div className="sub-muted">{t('subscription.localDirectShadowrocketHint')}</div>
            </>
          )}
        </>
      }
    />
  );
}
