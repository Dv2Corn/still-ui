import { Steps, Typography } from 'antd';

import type { SubRotationReport } from '@/generated/types';

// Step names and details stay in English: they are the same diagnostic lines the CLI prints.
export default function RotationCheckReport({ report }: { report: SubRotationReport }) {
  return (
    <>
      <Typography.Paragraph>
        <code>{report.client}</code>
      </Typography.Paragraph>
      <Steps
        orientation="vertical"
        size="small"
        items={report.steps.map((step) => ({
          title: step.name,
          content: <span dir="ltr">{step.detail}</span>,
          status: step.ok ? 'finish' : 'error',
        }))}
      />
    </>
  );
}
