#!/usr/bin/env bash
#
# Build ps5ctl for linux/amd64 and install it into the LXC that runs it on
# the Proxmox host, keeping the file capabilities the Bluetooth path needs.
#
#   scripts/deploy-lxc.sh                 # host root@192.168.1.55, CT 105
#   PVE_HOST=root@pve CT=105 scripts/deploy-lxc.sh
#
# Inside the container: Alpine, openrc service `ps5ctl` running as user
# ps5ctl with /usr/local/bin/ps5ctl and /var/lib/ps5ctl. Replacing the binary
# drops its file capabilities, so setcap is re-applied every time (libcap is
# installed there). Two backups of the previous binary are kept.
set -euo pipefail

cd "$(dirname "$0")/.."

PVE_HOST="${PVE_HOST:-root@192.168.1.55}"
CT="${CT:-105}"
VERSION="$(git describe --always --dirty)-$(date +%Y%m%d%H%M)"
OUT="${TMPDIR:-${TEMP:-/tmp}}/ps5ctl-linux-amd64"

echo "==> building $VERSION"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o "$OUT" ./cmd/ps5ctl

echo "==> copying to $PVE_HOST"
scp -q "$OUT" "$PVE_HOST:/tmp/ps5ctl.new"

echo "==> installing into CT $CT"
ssh "$PVE_HOST" "pct push $CT /tmp/ps5ctl.new /usr/local/bin/ps5ctl.new --perms 0755 && rm -f /tmp/ps5ctl.new && pct exec $CT -- sh -c '
set -e
cd /usr/local/bin
cp -a ps5ctl ps5ctl.bak-\$(date +%Y%m%d%H%M)
mv ps5ctl.new ps5ctl
chown root:root ps5ctl
chmod 0755 ps5ctl
setcap cap_net_raw,cap_net_admin+eip ps5ctl
getcap ps5ctl
ls -t ps5ctl.bak-* | tail -n +3 | xargs -r rm -f
rc-service ps5ctl restart >/dev/null
sleep 3
rc-service ps5ctl status
./ps5ctl version
echo --- status
wget -qO- http://127.0.0.1:8580/status | head -30
echo
echo --- log
grep -v \" DEBUG \" /var/lib/ps5ctl/logs/ps5ctl.log | tail -5
'"
