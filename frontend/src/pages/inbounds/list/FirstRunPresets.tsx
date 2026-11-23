import { useTranslation } from 'react-i18next';
import { Button, Card, Col, Row, Typography } from 'antd';

import { activateOnKey } from '@/utils/a11y';
import { INBOUND_PRESETS, type InboundPreset } from '@/lib/xray/inbound-presets';

export default function FirstRunPresets({
  onAddInbound,
}: {
  onAddInbound: (preset?: InboundPreset) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="card-empty first-run-presets">
      <Typography.Title level={4}>{t('pages.inbounds.firstRun.title')}</Typography.Title>
      <Row gutter={[16, 16]} justify="center">
        {INBOUND_PRESETS.map((preset) => (
          <Col key={preset} xs={24} md={8}>
            <Card
              hoverable
              role="button"
              tabIndex={0}
              onClick={() => onAddInbound(preset)}
              onKeyDown={activateOnKey(() => onAddInbound(preset))}
            >
              <Typography.Text strong>
                {t(`pages.inbounds.form.presetOptions.${preset}`)}
              </Typography.Text>
            </Card>
          </Col>
        ))}
      </Row>
      <Button type="link" onClick={() => onAddInbound()}>
        {t('pages.inbounds.firstRun.manual')}
      </Button>
    </div>
  );
}
