import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { SubRotationReport } from '@/generated/types';
import RotationCheckReport from '@/pages/clients/RotationCheckReport';
import { renderWithProviders } from './test-utils';

const staleOldLink: SubRotationReport = {
  passed: false,
  client: 'still-ui-rotation-check-1767225600000',
  steps: [
    { name: 'fetch its subscription', ok: true, detail: 'http://127.0.0.1:2096/sub/old' },
    {
      name: 'old link answers 404',
      ok: false,
      detail: 'HTTP 200, 512 bytes: the old link still answers',
    },
    { name: 'delete the test client', ok: true, detail: 'still-ui-rotation-check-1767225600000' },
  ],
};

function stepItem(name: string): HTMLElement {
  const item = screen.getByText(name).closest('.ant-steps-item');
  if (!(item instanceof HTMLElement)) throw new Error(`no step item for ${name}`);
  return item;
}

// The admin must see which step broke and why, and that cleanup still ran after it.
describe('RotationCheckReport', () => {
  it('marks the failing step with its detail and keeps the later cleanup step passed', () => {
    renderWithProviders(<RotationCheckReport report={staleOldLink} />);

    const failed = stepItem('old link answers 404');
    expect(failed.className).toContain('ant-steps-item-error');
    expect(failed.textContent).toContain('HTTP 200, 512 bytes: the old link still answers');
    expect(stepItem('fetch its subscription').className).toContain('ant-steps-item-finish');
    expect(stepItem('delete the test client').className).toContain('ant-steps-item-finish');
    expect(
      screen.getByText('still-ui-rotation-check-1767225600000', { selector: 'code' }),
    ).toBeTruthy();
  });
});
