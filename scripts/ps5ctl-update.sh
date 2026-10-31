#!/bin/sh
#
# Install a released ps5ctl binary from GitHub Releases inside the LXC that
# runs it (Alpine, openrc service `ps5ctl`). Installed there as
# /usr/local/bin/ps5ctl-update by scripts/deploy-lxc.sh.
#
#   ps5ctl-update              # newest release (prereleases excluded)
#   ps5ctl-update v0.1.1       # that tag, prereleases (v0.1.1-rc1) included
#   ps5ctl-update --dry-run v0.1.1
#
# The release workflow attaches ps5ctl-linux-<arch> and SHA256SUMS to every
# v* tag. The binary is checked against SHA256SUMS and must run before it
# replaces the old one; setcap is re-applied (a new file has no
# capabilities); if the service does not come back healthy on the new
# version, the previous binary is restored. Two backups are kept.
set -eu

REPO="${PS5CTL_REPO:-Protomothis/smartthings-ps5-control}"
BIN=/usr/local/bin/ps5ctl
HEALTH=http://127.0.0.1:8580/healthz

dry=0
if [ "${1:-}" = "--dry-run" ]; then
	dry=1
	shift
fi
tag="${1:-latest}"

case "$(uname -m)" in
x86_64) arch=amd64 ;;
aarch64) arch=arm64 ;;
*) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
asset="ps5ctl-linux-$arch"

if ! command -v curl >/dev/null 2>&1 || [ ! -s /etc/ssl/certs/ca-certificates.crt ]; then
	echo "==> installing curl and ca-certificates"
	apk add --no-cache -q curl ca-certificates
fi

if [ "$tag" = "latest" ]; then
	# /releases/latest redirects to the newest non-prerelease tag.
	tag="$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
	tag="${tag##*/}"
	case "$tag" in v*) ;; *) echo "could not resolve the latest release" >&2; exit 1 ;; esac
fi
base="https://github.com/$REPO/releases/download/$tag"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "==> downloading $tag/$asset"
curl -fsSL --retry 3 -o "$tmp/$asset" "$base/$asset"
curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
(cd "$tmp" && grep " $asset\$" SHA256SUMS | sha256sum -c -s) || { echo "checksum mismatch for $asset" >&2; exit 1; }
chmod 0755 "$tmp/$asset"
new_version="$("$tmp/$asset" version)"
old_version="$("$BIN" version 2>/dev/null || echo none)"
echo "    verified, reports $new_version (installed: $old_version)"

if [ "$dry" = 1 ]; then
	echo "==> dry run: nothing installed"
	exit 0
fi

backup="$BIN.bak-$(date +%Y%m%d%H%M%S)"
[ -e "$BIN" ] && cp -a "$BIN" "$backup"
install -o root -g root -m 0755 "$tmp/$asset" "$BIN.new"
setcap cap_net_raw,cap_net_admin+eip "$BIN.new"
mv -f "$BIN.new" "$BIN"
# keep the two newest backups
ls -t "$BIN".bak-* 2>/dev/null | tail -n +3 | xargs -r rm -f

echo "==> restarting ps5ctl"
rc-service ps5ctl restart >/dev/null
for _ in 1 2 3 4 5 6 7 8 9 10; do
	sleep 1
	if wget -qO- "$HEALTH" 2>/dev/null | grep -q "\"version\": \"$new_version\""; then
		echo "==> healthy on $new_version"
		exit 0
	fi
done

echo "!!  $new_version did not come up healthy; restoring $old_version" >&2
if [ -e "$backup" ]; then
	cp -a "$backup" "$BIN"
	setcap cap_net_raw,cap_net_admin+eip "$BIN"
	rc-service ps5ctl restart >/dev/null
fi
exit 1
