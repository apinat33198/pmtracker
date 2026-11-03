#!/bin/sh
# Install the PM tracker in a Proxmox container, from your Mac:
#
#   deploy/install.sh root@192.168.50.158 pm.example.com
#
# It switches the container to SSH key login (asking for the root password
# once), builds the tracker, installs it as a service, and can set up a
# Cloudflare Tunnel for the hostname. Safe to run again. Later updates:
# deploy/push.sh.
#
# Optional, next to README.md (git ignores both):
#   service-account.json  Google service-account key; without it the tracker
#                         runs and syncs calendars once the key is added
#   config.json           created for you on the first run; edit and re-run
set -eu
usage="usage: deploy/install.sh root@container pm.example.com"
host=${1:?$usage}
fqdn=${2:?$usage}
cd "$(dirname "$0")/.."
case $fqdn in
*[!a-zA-Z0-9.-]* | *..* | .* | *.) echo "$fqdn isn't a hostname like pm.example.com" >&2; exit 1 ;;
*.*) ;;
*) echo "$fqdn isn't a hostname like pm.example.com" >&2; exit 1 ;;
esac

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() {
	printf '\n\033[1;31m%s\033[0m\n' "$*" >&2
	exit 1
}

# --- Go on this Mac -----------------------------------------------------------
v=$(go env GOVERSION 2>/dev/null) || die "Go isn't installed. Install it (brew install go) and run this again."
case $v in
go1.1[0-9]* | go1.2[0-5] | go1.2[0-5].*) die "$v no longer gets security fixes. Install Go 1.26 or newer (brew upgrade go) and run this again." ;;
esac

# --- SSH key login ------------------------------------------------------------
say "Checking SSH access to $host"
if ! ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" true 2>/dev/null; then
	if ! ls "$HOME"/.ssh/id_*.pub >/dev/null 2>&1; then
		ssh-keygen -q -t ed25519 -N "" -f "$HOME/.ssh/id_ed25519"
	fi
	echo "Type the container's root password once; after this the script logs in with your SSH key."
	if ! ssh-copy-id -o ConnectTimeout=8 "$host"; then
		key=$(cat "$(ls "$HOME"/.ssh/id_*.pub | head -1)")
		die "Couldn't log in with the password. In the container's console (Proxmox > the container > Console) run:

  mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '$key' >> ~/.ssh/authorized_keys

then run this script again."
	fi
fi
ssh -o BatchMode=yes "$host" true || die "Still can't log in to $host with the SSH key."

# --- Container basics ---------------------------------------------------------
say "Preparing the container"
case $(ssh "$host" uname -m) in
x86_64) goarch=amd64 ;;
aarch64) goarch=arm64 ;;
*) die "Unsupported container architecture." ;;
esac
ssh "$host" 'export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq && apt-get install -y -qq ca-certificates curl >/dev/null
	echo "Debian packages OK"'

# --- Config -------------------------------------------------------------------
if [ ! -f config.json ]; then
	say "Writing config.json"
	printf 'Google Calendar ID to read (press Enter to add calendars later under Admin): '
	read -r cal
	{
		printf '{\n'
		printf '  "timezone": "Asia/Bangkok",\n'
		printf '  "listen": "127.0.0.1:8080",\n'
		printf '  "public_url": "https://%s",\n' "$fqdn"
		printf '  "data_file": "/var/lib/pmtracker/data.json",\n'
		printf '  "google": {"credentials_file": "/etc/pmtracker/service-account.json"}'
		if [ -n "$cal" ]; then
			printf ',\n  "calendars": [{"id": "%s", "label": "PM calendar"}]' "$cal"
		fi
		printf '\n}\n'
	} >config.json
	echo "Saved config.json (git ignores it). Edit it and run this script again to change settings."
else
	grep -q "\"https://$fqdn\"" config.json || echo "Note: config.json's public_url isn't https://$fqdn; keeping your file as it is."
fi

# --- Build and copy -----------------------------------------------------------
say "Building for linux/$goarch with $v"
bin=bin/pmtracker-linux-$goarch
GOOS=linux GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$bin" ./cmd/pmtracker

say "Copying to the container"
ssh "$host" 'rm -rf /root/pmtracker-install && mkdir -m 700 /root/pmtracker-install'
scp -q "$bin" deploy/pmtracker.service config.json "$host:/root/pmtracker-install/"
if [ -f service-account.json ]; then
	scp -q service-account.json "$host:/root/pmtracker-install/"
else
	echo "No service-account.json here: the tracker will run without Google Calendar for now."
fi

# --- Install and start --------------------------------------------------------
say "Installing the service"
ssh "$host" "GOARCH=$goarch sh -s" <<'REMOTE'
set -eu
cd /root/pmtracker-install
id pmtracker >/dev/null 2>&1 || useradd --system --user-group --home /var/lib/pmtracker --shell /usr/sbin/nologin pmtracker
install -m 0755 "pmtracker-linux-$GOARCH" /usr/local/bin/pmtracker
install -d -m 0750 -o root -g pmtracker /etc/pmtracker
install -m 0640 -o root -g pmtracker config.json /etc/pmtracker/config.json
if [ -f service-account.json ]; then
	install -m 0640 -o root -g pmtracker service-account.json /etc/pmtracker/service-account.json
fi
install -d -m 0700 -o pmtracker -g pmtracker /var/lib/pmtracker
install -m 0644 pmtracker.service /etc/systemd/system/pmtracker.service
cd / && rm -rf /root/pmtracker-install
systemctl daemon-reload
systemctl enable pmtracker >/dev/null 2>&1
systemctl restart pmtracker
sleep 3
if ! systemctl is-active --quiet pmtracker; then
	journalctl -u pmtracker -n 15 --no-pager || true
	if systemctl show pmtracker -p ExecMainStatus | grep -q '=226'; then
		echo
		echo "The service couldn't start its sandbox. In Proxmox turn on the container's"
		echo "Options > Features > Nesting, reboot the container and run this script again."
	fi
	exit 1
fi
curl -fsS http://127.0.0.1:8080/healthz >/dev/null
echo "The tracker is running."
if [ -f /etc/pmtracker/service-account.json ] && grep -q '"calendars"' /etc/pmtracker/config.json; then
	runuser -u pmtracker -- /usr/local/bin/pmtracker -config /etc/pmtracker/config.json check || true
fi
REMOTE

# --- Cloudflare Tunnel --------------------------------------------------------
printf '\nSet up a Cloudflare Tunnel so https://%s reaches the tracker? [Y/n] ' "$fqdn"
read -r ans
case $ans in
[nN]*) tunnel=skipped ;;
*)
	tunnel=yes
	say "Setting up the Cloudflare Tunnel"
	ssh "$host" "FQDN=$fqdn sh -s" <<'REMOTE' || tunnel=failed
set -eu
export DEBIAN_FRONTEND=noninteractive
if ! command -v cloudflared >/dev/null; then
	mkdir -p -m 0755 /usr/share/keyrings
	curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg
	echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' >/etc/apt/sources.list.d/cloudflared.list
	apt-get update -qq && apt-get install -y -qq cloudflared >/dev/null
fi
if [ -f /etc/cloudflared/config.yml ] && ! grep -q "hostname: $FQDN" /etc/cloudflared/config.yml; then
	echo "/etc/cloudflared/config.yml already exists for something else; leaving it alone."
	echo "Add a route for $FQDN to http://127.0.0.1:8080 to that tunnel instead."
	exit 0
fi
if [ ! -f /root/.cloudflared/cert.pem ]; then
	echo
	echo ">>> Open the link below in your browser, sign in to Cloudflare and pick the domain of $FQDN."
	echo ">>> This window carries on by itself once you've authorised it."
	cloudflared tunnel login
fi
id=$(cloudflared tunnel list --name pmtracker 2>/dev/null | awk '$2 == "pmtracker" { print $1; exit }')
if [ -z "$id" ]; then
	cloudflared tunnel create pmtracker
	id=$(cloudflared tunnel list --name pmtracker 2>/dev/null | awk '$2 == "pmtracker" { print $1; exit }')
fi
[ -n "$id" ] || { echo "Couldn't find the pmtracker tunnel."; exit 1; }
if [ ! -f "/root/.cloudflared/$id.json" ] && [ ! -f "/etc/cloudflared/$id.json" ]; then
	echo "A tunnel named pmtracker already exists in your Cloudflare account but its credentials aren't on this container."
	echo "Delete it in the dashboard (Networking > Tunnels) or rename it, then run this script again."
	exit 1
fi
install -d -m 0755 /etc/cloudflared
[ -f "/etc/cloudflared/$id.json" ] || install -m 0600 "/root/.cloudflared/$id.json" "/etc/cloudflared/$id.json"
cat >/etc/cloudflared/config.yml <<EOF
tunnel: $id
credentials-file: /etc/cloudflared/$id.json
ingress:
  - hostname: $FQDN
    service: http://127.0.0.1:8080
  - service: http_status:404
EOF
if ! cloudflared tunnel route dns pmtracker "$FQDN"; then
	echo "Couldn't add the DNS record for $FQDN. If a record with that name already exists"
	echo "in Cloudflare DNS, delete it (or pick another hostname) and run this script again."
	exit 1
fi
if [ -f /etc/systemd/system/cloudflared.service ]; then
	systemctl restart cloudflared
else
	cloudflared service install
fi
sleep 5
systemctl is-active --quiet cloudflared && echo "cloudflared is running."
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "https://$FQDN/healthz" || true)
echo "https://$FQDN/healthz answered HTTP $code (200 means the tunnel works; DNS can take a minute)."
REMOTE
	;;
esac

# --- Done ---------------------------------------------------------------------
say "Done"
# The newest setup link in the log, if the tracker still has no accounts.
link=$(ssh "$host" "curl -s -o /dev/null -w '%{redirect_url}' http://127.0.0.1:8080/ | grep -q '/setup\$' &&
	journalctl -u pmtracker --no-pager -o cat | grep -o 'https\{0,1\}://[^ ]*/setup?token=[^ ]*' | tail -1" || true)
if [ -n "$link" ]; then
	echo "Create the first admin here (the link works until that account exists):"
	echo
	echo "  $link"
else
	echo "Accounts already exist. Sign in at https://$fqdn"
fi
case $tunnel in
skipped) echo "(No tunnel yet: until you add one, the tracker only answers inside the container.)" ;;
failed) echo "(The tunnel step didn't finish; see its messages above. The tracker itself is installed.)" ;;
esac
echo
echo "Logs: ssh $host journalctl -u pmtracker -f"
echo "Updates later: deploy/push.sh $host"
