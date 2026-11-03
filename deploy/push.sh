#!/bin/sh
# Build on your Mac and update the tracker in the Proxmox container.
#
#   deploy/push.sh root@pm-tracker            # amd64 container
#   ARCH=arm64 deploy/push.sh root@pm-tracker
#
# For updates. The first install follows the steps at the top of
# deploy/pmtracker.service. The data file is copied to data.json.before-update
# before the restart, so a bad release can be rolled back.
set -eu
host=${1:?usage: deploy/push.sh user@container}
arch=${ARCH:-amd64}
cd "$(dirname "$0")/.."

v=$(go env GOVERSION)
case $v in
go1.1[0-9]* | go1.2[0-5] | go1.2[0-5].*)
	echo "$v no longer gets security fixes; install Go 1.26 or newer first." >&2
	exit 1
	;;
esac

go test ./...
out=bin/pmtracker-linux-$arch
GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/pmtracker
echo "Built $out with $v ($(git rev-parse --short HEAD))"

scp -q "$out" "$host:/tmp/pmtracker.new"
ssh "$host" 'set -e
	data=/var/lib/pmtracker/data.json
	[ -f "$data" ] && cp -p "$data" "$data.before-update"
	install -m 0755 /tmp/pmtracker.new /usr/local/bin/pmtracker
	rm -f /tmp/pmtracker.new
	systemctl restart pmtracker
	sleep 1
	systemctl --no-pager --lines=5 status pmtracker'
