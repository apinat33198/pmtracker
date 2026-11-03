#!/bin/sh
# Install or update the PM tracker in a Proxmox container. Either:
#
#   In the container (as root, in a clone of this repo):
#     deploy/install.sh pm.example.com
#
#   From your Mac, over SSH:
#     deploy/install.sh root@192.168.50.158 pm.example.com
#
# It builds the tracker (installing a current Go in the container if needed),
# installs it as a service, and can set up a Cloudflare Tunnel for the
# hostname. Safe to run again: that's also how you update (git pull first).
#
# Optional, next to README.md (git ignores both):
#   service-account.json  Google service-account key; without it the tracker
#                         runs and syncs calendars once the key is added
#   config.json           created for you on the first run; edit and re-run
set -eu
usage="usage: deploy/install.sh pm.example.com              (in the container)
       deploy/install.sh root@container pm.example.com  (from your Mac)"
case $# in
1) mode=local host=this-container fqdn=$1 ;;
2) mode=remote host=$1 fqdn=$2 ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
cd "$(dirname "$0")/.."
case $fqdn in
*[!a-zA-Z0-9.-]* | *..* | .* | *.) bad=1 ;;
*.*) bad= ;;
*) bad=1 ;;
esac
[ -z "$bad" ] || {
	echo "$fqdn isn't a hostname like pm.example.com" >&2
	echo "$usage" >&2
	exit 2
}

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() {
	printf '\n\033[1;31m%s\033[0m\n' "$*" >&2
	exit 1
}

# on_target VAR=value... : run the shell script on stdin in the container.
on_target() {
	if [ "$mode" = local ]; then
		env "$@" sh -s
	else
		ssh "$host" "$* sh -s"
	fi
}

# target_cmd 'command' : run one command in the container.
target_cmd() {
	if [ "$mode" = local ]; then
		sh -c "$1"
	else
		ssh "$host" "$1"
	fi
}

# go_minor go1.27.3 -> 27
go_minor() { echo "$1" | sed -n 's/^go1\.\([0-9][0-9]*\).*/\1/p'; }

# install_go amd64 : the current Go release into /usr/local/go (container only).
install_go() {
	ver=$(curl -fsSL "${GO_VERSION_URL:-https://go.dev/VERSION?m=text}" | head -1)
	case $ver in go1.*) ;; *) die "Couldn't look up the current Go release (no internet in the container?)." ;; esac
	file=$ver.linux-$1.tar.gz
	echo "Installing $ver into ${GO_PARENT:-/usr/local}/go"
	tmp=$(mktemp -d)
	curl -fsSL -o "$tmp/$file" "${GO_DL:-https://dl.google.com/go}/$file" || die "Couldn't download $file."
	sum=$(curl -fsSL "${GO_DL:-https://dl.google.com/go}/$file.sha256") || die "Couldn't download the checksum of $file."
	echo "$sum  $tmp/$file" | sha256sum -c - >/dev/null || die "$file doesn't match its published checksum."
	rm -rf "${GO_PARENT:-/usr/local}/go"
	tar -C "${GO_PARENT:-/usr/local}" -xzf "$tmp/$file"
	rm -rf "$tmp"
	if [ -z "${GO_PARENT:-}" ]; then
		echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
	fi
}

if [ "$mode" = local ]; then
	# --- In the container -------------------------------------------------------
	[ "$(id -u)" = 0 ] || die "Run this as root in the container."
	command -v systemctl >/dev/null || die "No systemd here. Run this inside the Proxmox container, or from your Mac with root@container as the first argument."
	say "Preparing the container"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq && apt-get install -y -qq ca-certificates curl tar >/dev/null
	echo "Debian packages OK"
	case $(uname -m) in
	x86_64) goarch=amd64 ;;
	aarch64) goarch=arm64 ;;
	*) die "Unsupported architecture $(uname -m)." ;;
	esac
	PATH=${GO_PARENT:-/usr/local}/go/bin:$PATH
	v=$(go env GOVERSION 2>/dev/null || true)
	m=$(go_minor "$v")
	if [ -z "$m" ] || [ "$m" -lt 26 ]; then
		if [ -n "$v" ]; then say "Installing a current Go ($v is too old)"; else say "Installing Go to build with"; fi
		install_go "$goarch"
		v=$(go env GOVERSION)
	fi
else
	# --- From the Mac -----------------------------------------------------------
	v=$(go env GOVERSION 2>/dev/null) || die "Go isn't installed. Install it (brew install go) and run this again."
	m=$(go_minor "$v")
	if [ -z "$m" ] || [ "$m" -lt 26 ]; then
		die "$v no longer gets security fixes. Install Go 1.26 or newer (brew upgrade go) and run this again."
	fi

	say "Checking SSH access to $host"
	if ! ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" true 2>/dev/null; then
		if ! ls "$HOME"/.ssh/id_*.pub >/dev/null 2>&1; then
			ssh-keygen -q -t ed25519 -N "" -f "$HOME/.ssh/id_ed25519"
		fi
		pub=$(ls -t "$HOME"/.ssh/id_*.pub | head -1)
		echo "This Mac's SSH key isn't on the container yet. Trying the root password once"
		echo "(Debian containers usually refuse root passwords over SSH; if so, see below)."
		if ! ssh-copy-id -i "$pub" -o NumberOfPasswordPrompts=1 -o ConnectTimeout=8 "$host" 2>/dev/null; then
			die "The container doesn't accept the root password over SSH (Debian's default), so add this Mac's key once. Either:

  a) From this Mac through the Proxmox host (asks for the Proxmox root password; <CTID> is the container's number):

     ssh root@<proxmox-host-IP> \"pct exec <CTID> -- sh -c 'mkdir -p -m 700 /root/.ssh && cat >> /root/.ssh/authorized_keys'\" < $pub

  b) Or in the Proxmox web UI: the container > Console, log in as root, and run:

     mkdir -p -m 700 ~/.ssh && echo '$(cat "$pub")' >> ~/.ssh/authorized_keys

Then run this script again. Or skip the Mac: clone the repo in the container and run deploy/install.sh $fqdn there."
		fi
	fi
	ssh -o BatchMode=yes "$host" true || die "Still can't log in to $host with the SSH key."

	say "Preparing the container"
	case $(ssh "$host" uname -m) in
	x86_64) goarch=amd64 ;;
	aarch64) goarch=arm64 ;;
	*) die "Unsupported container architecture." ;;
	esac
	ssh "$host" 'export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq && apt-get install -y -qq ca-certificates curl >/dev/null
		echo "Debian packages OK"'
fi

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
	chmod 600 config.json
	echo "Saved config.json (git ignores it). Edit it and run this script again to change settings."
else
	grep -q "\"https://$fqdn\"" config.json || echo "Note: config.json's public_url isn't https://$fqdn; keeping your file as it is."
fi

# --- Build and stage ----------------------------------------------------------
say "Building for linux/$goarch with $v"
bin=bin/pmtracker-linux-$goarch
# -p 1 keeps memory low enough for a 512 MB container.
GOOS=linux GOARCH=$goarch CGO_ENABLED=0 go build -p 1 -trimpath -ldflags="-s -w" -o "$bin" ./cmd/pmtracker

files="$bin deploy/pmtracker.service config.json"
if [ -f service-account.json ]; then
	files="$files service-account.json"
else
	echo "No service-account.json here: the tracker will run without Google Calendar for now."
fi
target_cmd 'rm -rf /root/pmtracker-install && mkdir -m 700 /root/pmtracker-install'
if [ "$mode" = local ]; then
	# shellcheck disable=SC2086 # word splitting of $files is intended
	cp $files /root/pmtracker-install/
else
	say "Copying to the container"
	# shellcheck disable=SC2086
	scp -q $files "$host:/root/pmtracker-install/"
fi

# --- Install and start --------------------------------------------------------
say "Installing the service"
on_target "GOARCH=$goarch" <<'REMOTE'
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
printf '\nSet up a Cloudflare Tunnel in this container for https://%s?\n(Answer n if cloudflared already runs on another machine; add a route there instead.) [Y/n] ' "$fqdn"
read -r ans
case $ans in
[nN]*) tunnel=skipped ;;
*)
	tunnel=yes
	say "Setting up the Cloudflare Tunnel"
	on_target "FQDN=$fqdn" <<'REMOTE' || tunnel=failed
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
	echo ">>> Open the link below in a browser on any computer, sign in to Cloudflare and pick the domain of $FQDN."
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
link=$(target_cmd "curl -s -o /dev/null -w '%{redirect_url}' http://127.0.0.1:8080/ | grep -q '/setup\$' &&
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
if [ "$mode" = local ]; then
	echo "Logs: journalctl -u pmtracker -f"
	echo "Updates later: git pull && deploy/install.sh $fqdn"
else
	echo "Logs: ssh $host journalctl -u pmtracker -f"
	echo "Updates later: deploy/push.sh $host"
fi
