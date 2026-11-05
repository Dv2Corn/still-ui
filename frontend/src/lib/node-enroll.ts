// install.sh is a release file listed in SHA256SUMS; it verifies the archive
// it installs, so the node runs a checked release.
export const NODE_INSTALL_SCRIPT =
  'https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh';

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

// The invitation goes through the installer's environment, not its command
// line, which other local users can read (ps, /proc/<pid>/cmdline).
export function nodeEnrollCommand(invitation: string): string {
  return `STILL_NODE_INVITE=${shellQuote(invitation)} bash <(curl -fsSL ${NODE_INSTALL_SCRIPT}) node-enroll`;
}
