import { describe, it, expect } from 'vitest';
import {
  buildScriptCommand,
  buildDockerRun,
  buildDockerCompose,
  type InstallOptions,
} from './install';

const base: InstallOptions = {
  method: 'script',
  version: '',
  enableFail2ban: true,
  panelPort: '',
  webBasePath: '',
};

describe('buildScriptCommand', () => {
  it("uses still-ui's install.sh for the latest version", () => {
    expect(buildScriptCommand(base)).toBe(
      'bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)',
    );
  });

  it('pins a specific version by passing the tag to install.sh', () => {
    const cmd = buildScriptCommand({ ...base, version: 'v1.5.0' });
    expect(cmd).toBe(
      'bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v1.5.0',
    );
  });

  it('never points at upstream 3x-ui', () => {
    expect(buildScriptCommand(base)).not.toMatch(/mhsanaei/i);
  });
});

describe('buildDockerRun', () => {
  it('reflects fail2ban, port, and base path options', () => {
    const cmd = buildDockerRun({
      ...base,
      enableFail2ban: false,
      panelPort: '8443',
      webBasePath: '/panel',
    });
    expect(cmd).toContain('XUI_ENABLE_FAIL2BAN=false');
    expect(cmd).toContain('XUI_PORT=8443');
    expect(cmd).toContain('XUI_INIT_WEB_BASE_PATH=/panel');
    expect(cmd).toContain('still-ui:local');
    expect(cmd).not.toMatch(/ghcr\.io|mhsanaei/i);
    expect(cmd).toContain('-v $PWD/db/:/etc/x-ui/');
  });

  it('builds the image from the chosen release', () => {
    const cmd = buildDockerRun({ ...base, version: 'v1.5.0' });
    expect(cmd.split('\n')[0]).toBe(
      'docker build -t still-ui:local https://github.com/Dv2Corn/still-ui.git#v1.5.0',
    );
  });

  it('omits unset port and path', () => {
    const cmd = buildDockerRun(base);
    expect(cmd).not.toContain('XUI_PORT');
    expect(cmd).not.toContain('XUI_INIT_WEB_BASE_PATH');
  });
});

describe('buildDockerCompose', () => {
  it('produces valid-looking compose with the image and volumes', () => {
    const yaml = buildDockerCompose({ ...base, panelPort: '2096' });
    expect(yaml).toContain('build: https://github.com/Dv2Corn/still-ui.git#main');
    expect(yaml).toContain('image: still-ui:local');
    expect(yaml).toContain('network_mode: host');
    expect(yaml).toContain("XUI_PORT: '2096'");
    expect(yaml).toContain('- ./db/:/etc/x-ui/');
  });
});
