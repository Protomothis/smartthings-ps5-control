#!/usr/bin/env bash
#
# Deploy a released ps5ctl into the LXC that runs it on the Proxmox host.
# Nothing is built here: the release workflow builds the binaries on a v*
# tag, and the container downloads one, checks it against SHA256SUMS,
# installs it with the file capabilities the Bluetooth path needs, and rolls
# back if the service does not come up healthy (scripts/ps5ctl-update.sh).
#
#   scripts/deploy-lxc.sh v0.1.1          # that release (or v0.1.1-rc1)
#   scripts/deploy-lxc.sh                 # newest non-prerelease
#   scripts/deploy-lxc.sh --dry-run v0.1.1
#   PVE_HOST=root@pve CT=105 scripts/deploy-lxc.sh v0.1.1
#
# The installer itself is copied into the container on every run, so it is
# always the one from this checkout; inside the container it is
# /usr/local/bin/ps5ctl-update and can be run there directly.
set -euo pipefail

cd "$(dirname "$0")/.."

PVE_HOST="${PVE_HOST:-root@192.168.1.55}"
CT="${CT:-105}"

echo "==> updating the installer in CT $CT"
scp -q scripts/ps5ctl-update.sh "$PVE_HOST:/tmp/ps5ctl-update"
ssh "$PVE_HOST" "pct push $CT /tmp/ps5ctl-update /usr/local/bin/ps5ctl-update --perms 0755 && rm -f /tmp/ps5ctl-update"

echo "==> ps5ctl-update $*"
# printf %q keeps the arguments intact through the two shells.
ssh "$PVE_HOST" "pct exec $CT -- /bin/sh -lc $(printf '%q' "/usr/local/bin/ps5ctl-update $*")"
ssh "$PVE_HOST" "pct exec $CT -- /bin/sh -c 'grep -v \" DEBUG \" /var/lib/ps5ctl/logs/ps5ctl.log | tail -5'"
