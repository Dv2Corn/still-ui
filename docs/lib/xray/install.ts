// Pure builders for still-ui install commands (script + Docker). No React/DOM.

export type InstallMethod = 'script' | 'docker';

export interface InstallOptions {
  method: InstallMethod;
  /** A release tag like `v1.5.0`, or empty/`latest` for the latest release. */
  version: string;
  enableFail2ban: boolean;
  panelPort: string;
  webBasePath: string;
}

const INSTALLER_URL = 'https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh';
// still-ui publishes no container image: Docker builds one from the repository
// (its Dockerfile pins still-ui's own Xray build) at the chosen release tag.
const REPO_GIT = 'https://github.com/Dv2Corn/still-ui.git';
const IMAGE = 'still-ui:local';

function buildContext(version: string): string {
  return `${REPO_GIT}#${isLatest(version) ? 'main' : version.trim()}`;
}

function isLatest(version: string): boolean {
  const v = version.trim().toLowerCase();
  return v === '' || v === 'latest';
}

/**
 * The one-line script install command. install.sh reads the version as its
 * first argument: empty = the latest release, a tag like `v1.5.0` = that
 * release. Either way it installs a release archive whose checksum matches.
 */
export function buildScriptCommand(options: InstallOptions): string {
  if (isLatest(options.version)) {
    return `bash <(curl -Ls ${INSTALLER_URL})`;
  }
  return `bash <(curl -Ls ${INSTALLER_URL}) ${options.version.trim()}`;
}

/** `docker build` + `docker run` commands reflecting the chosen options. */
export function buildDockerRun(options: InstallOptions): string {
  const lines = ['docker run -itd'];
  lines.push(`  -e XRAY_VMESS_AEAD_FORCED=false`);
  lines.push(`  -e XUI_ENABLE_FAIL2BAN=${options.enableFail2ban ? 'true' : 'false'}`);
  if (options.panelPort.trim()) lines.push(`  -e XUI_PORT=${options.panelPort.trim()}`);
  if (options.webBasePath.trim())
    lines.push(`  -e XUI_INIT_WEB_BASE_PATH=${options.webBasePath.trim()}`);
  lines.push(`  -v $PWD/db/:/etc/x-ui/`);
  lines.push(`  -v $PWD/cert/:/root/cert/`);
  lines.push(`  --network=host`);
  lines.push(`  --restart=unless-stopped`);
  lines.push(`  --name still-ui`);
  lines.push(`  ${IMAGE}`);
  return `docker build -t ${IMAGE} ${buildContext(options.version)}\n${lines.join(' \\\n')}`;
}

/** A `docker-compose.yml` reflecting the chosen options. */
export function buildDockerCompose(options: InstallOptions): string {
  const env: string[] = [
    `      XRAY_VMESS_AEAD_FORCED: 'false'`,
    `      XUI_ENABLE_FAIL2BAN: '${options.enableFail2ban ? 'true' : 'false'}'`,
  ];
  if (options.panelPort.trim()) env.push(`      XUI_PORT: '${options.panelPort.trim()}'`);
  if (options.webBasePath.trim())
    env.push(`      XUI_INIT_WEB_BASE_PATH: '${options.webBasePath.trim()}'`);

  return [
    `services:`,
    `  still-ui:`,
    `    build: ${buildContext(options.version)}`,
    `    image: ${IMAGE}`,
    `    container_name: still-ui`,
    `    volumes:`,
    `      - ./db/:/etc/x-ui/`,
    `      - ./cert/:/root/cert/`,
    `    environment:`,
    ...env,
    `    network_mode: host`,
    `    restart: unless-stopped`,
  ].join('\n');
}
