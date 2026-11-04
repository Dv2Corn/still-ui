export const PROBE_INSTALL_SCRIPT =
  'https://raw.githubusercontent.com/Dv2Corn/still-ui/main/scripts/still-probe-install.sh';

// Inbound protocols the probe agent can dial; the panel refuses the rest.
export const PROBE_PROTOCOLS = ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria'];

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

// The token goes to the installer through its environment (STILL_PROBE_TOKEN),
// not its command line: other local users can read a command line (ps,
// /proc/<pid>/cmdline), but not another user's environment.
export function probeInstallCommand(panelUrl: string, token: string): string {
  return `STILL_PROBE_TOKEN=${shellQuote(token)} bash <(curl -fsSL ${PROBE_INSTALL_SCRIPT}) --panel ${shellQuote(panelUrl)}`;
}

export type ProbeBucket = 'ok' | 'fail' | 'mixed' | 'none';

export interface ProbePoint {
  key: string;
  at: number;
  ok: boolean;
}

// One cell per hour of the last seven days, oldest first.
export function probeHistoryBuckets(
  points: ProbePoint[],
  key: string,
  now: number,
  hours = 168,
): ProbeBucket[] {
  const hourMs = 3_600_000;
  const start = now - hours * hourMs;
  const oks = new Array<number>(hours).fill(0);
  const fails = new Array<number>(hours).fill(0);
  for (const p of points) {
    if (p.key !== key || p.at < start || p.at >= now) continue;
    const i = Math.floor((p.at - start) / hourMs);
    if (p.ok) oks[i] += 1;
    else fails[i] += 1;
  }
  return oks.map((ok, i) => {
    const fail = fails[i];
    if (ok === 0 && fail === 0) return 'none';
    if (fail === 0) return 'ok';
    if (ok === 0) return 'fail';
    return 'mixed';
  });
}
