import { describe, expect, it } from 'vitest';

import { NODE_INSTALL_SCRIPT, nodeEnrollCommand } from '@/lib/node-enroll';

describe('nodeEnrollCommand', () => {
  it('passes the invitation in the environment, quoted, and runs node-enroll', () => {
    const cmd = nodeEnrollCommand("still-node1.a'b$(id)");
    expect(cmd).toBe(
      `STILL_NODE_INVITE='still-node1.a'\\''b$(id)' bash <(curl -fsSL ${NODE_INSTALL_SCRIPT}) node-enroll`,
    );
  });

  it('installs from the release files, not from a branch', () => {
    expect(NODE_INSTALL_SCRIPT).toMatch(
      /^https:\/\/github\.com\/Dv2Corn\/still-ui\/releases\/latest\/download\/install\.sh$/,
    );
  });
});
