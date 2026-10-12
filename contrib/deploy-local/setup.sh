#!/bin/bash
# One-time setup, run as root on the Proxmox VE host (this file is self-contained, copy it alone): creates a long-lived LXC that runs spoved as a
# systemd service and accepts deploys from the "Deploy local" workflow (.github/workflows/deploy-local.yml).
#
#   setup.sh <vmid> [bridge] [storage]      e.g. setup.sh 300 vmbr4 local-zfs
#
# The deploy key is created here and only its public half stays in the container, locked to the
# spoved-deploy forced command (no shell, no forwarding). The script prints the three repository secrets
# to add, then deletes the private key from the host.
set -euo pipefail

VMID=${1:?usage: setup.sh <vmid> [bridge] [storage]}
BRIDGE=${2:-vmbr4}
STORAGE=${3:-local-zfs}

pct status "$VMID" >/dev/null 2>&1 && { echo "VMID $VMID already exists" >&2; exit 1; }

pveam update >/dev/null
TEMPLATE=$(pveam available --section system | awk '{print $2}' | grep '^debian-12-standard' | sort -V | tail -1)
pveam list local | grep -q "$TEMPLATE" || pveam download local "$TEMPLATE"

pct create "$VMID" "local:vztmpl/$TEMPLATE" \
    --hostname spoved --unprivileged 1 --features nesting=1 \
    --cores 4 --memory 4096 --swap 1024 --rootfs "$STORAGE:20" \
    --net0 "name=eth0,bridge=$BRIDGE,ip=dhcp" --onboot 1 --tags spoved
pct start "$VMID"
for _ in $(seq 30); do
    IP=$(pct exec "$VMID" -- hostname -I 2>/dev/null | awk '{print $1}') && [ -n "$IP" ] && break
    sleep 2
done
[ -n "${IP:-}" ] || { echo "the container got no address on $BRIDGE" >&2; exit 1; }

pct exec "$VMID" -- bash -euc '
    apt-get update -q && apt-get install -yq git curl openssh-server openssl >/dev/null
    adduser --system --group --shell /bin/bash --home /home/git git
    install -d -o git -g git -m 750 /var/lib/spoved /var/lib/spoved/data /etc/spoved
    install -d -m 700 /var/backups/spoved /root/.ssh
'

# app.ini is owned by git: on first start spoved writes the JWT secrets it generates into it
pct exec "$VMID" -- bash -euc "cat > /etc/spoved/app.ini" <<INI
APP_NAME = spoved
RUN_USER = git
RUN_MODE = prod
WORK_PATH = /var/lib/spoved

[server]
HTTP_PORT = 3000
ROOT_URL = http://$IP:3000/
START_SSH_SERVER = true
SSH_PORT = 2222
LFS_START_SERVER = true

[database]
DB_TYPE = sqlite3
PATH = /var/lib/spoved/data/spoved.db

[security]
INSTALL_LOCK = true
SECRET_KEY = $(openssl rand -hex 32)

[service]
DISABLE_REGISTRATION = true

[log]
MODE = console
LEVEL = Info
INI
pct exec "$VMID" -- bash -euc 'chown git:git /etc/spoved/app.ini && chmod 640 /etc/spoved/app.ini'

pct exec "$VMID" -- bash -euc "cat > /etc/systemd/system/spoved.service" <<'UNIT'
[Unit]
Description=spoved
After=network-online.target
Wants=network-online.target

[Service]
User=git
Group=git
WorkingDirectory=/var/lib/spoved
# the internal UNIX socket lives in /run/forgejo
RuntimeDirectory=forgejo
ExecStart=/usr/local/bin/spoved web --config /etc/spoved/app.ini --work-path /var/lib/spoved
Environment=USER=git HOME=/home/git
Restart=always
RestartSec=2s

[Install]
WantedBy=multi-user.target
UNIT
# enabled, not started: the first deploy brings the binary
pct exec "$VMID" -- systemctl enable spoved

pct exec "$VMID" -- bash -euc 'cat > /usr/local/sbin/spoved-deploy && chmod 755 /usr/local/sbin/spoved-deploy' <<'DEPLOY'
#!/bin/sh
# Installs the spoved binary read from stdin and restarts the service. It is the forced command of the
# deploy key (see setup.sh), so the key can do nothing else. Before the swap it copies the binary,
# app.ini and the SQLite database aside; if the new binary doesn't answer /api/healthz, all three are
# restored, because a newer build may already have migrated the database.
set -eu

BIN=/usr/local/bin/spoved
DATA=/var/lib/spoved
CONF=/etc/spoved/app.ini
BACKUPS=/var/backups/spoved
KEEP=5
MAX_BYTES=1000000000

# what the workflow sent as the ssh command (ref and commit), for the log only
info=$(printf '%s' "${SSH_ORIGINAL_COMMAND:-}" | tr -cd 'A-Za-z0-9._/@ -' | cut -c1-200)
log() { echo "spoved-deploy: $*"; logger -t spoved-deploy -- "$*" 2>/dev/null || true; }

new=$(mktemp /usr/local/bin/.spoved.XXXXXX)
trap 'rm -f "$new"' EXIT
head -c "$MAX_BYTES" > "$new"
chmod 755 "$new"
"$new" --version >/dev/null || { log "refused: the upload is not a working spoved binary"; exit 1; }

healthy() {
    i=0
    while [ $i -lt 60 ]; do
        curl -fs --noproxy "*" -o /dev/null http://127.0.0.1:3000/api/healthz && return 0
        sleep 2
        i=$((i + 1))
    done
    return 1
}

stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup="$BACKUPS/$stamp"
mkdir -p "$backup"
systemctl stop spoved
[ -f "$BIN" ] && cp -p "$BIN" "$backup/spoved"
cp -p "$CONF" "$backup/app.ini"
# the -wal file can hold the latest writes, so it is saved and restored with the database
for f in spoved.db spoved.db-wal spoved.db-shm; do
    [ -f "$DATA/data/$f" ] && cp -p "$DATA/data/$f" "$backup/$f"
done
# keep the newest $KEEP backups
ls -1d "$BACKUPS"/*/ | sort -r | tail -n +$((KEEP + 1)) | xargs -r rm -rf

mv -f "$new" "$BIN"
systemctl start spoved
if healthy; then
    log "deployed $info ($("$BIN" --version))"
    exit 0
fi

log "deploy of $info failed its health check; restoring backup $stamp"
journalctl -u spoved -n 40 --no-pager || true
systemctl stop spoved
if [ -f "$backup/spoved" ]; then
    cp -p "$backup/spoved" "$BIN"
    cp -p "$backup/app.ini" "$CONF"
    for f in spoved.db spoved.db-wal spoved.db-shm; do
        if [ -f "$backup/$f" ]; then cp -p "$backup/$f" "$DATA/data/$f"; else rm -f "$DATA/data/$f"; fi
    done
    systemctl start spoved
    healthy && log "previous version restored" || log "previous version did not come back either"
fi
exit 1
DEPLOY

KEYDIR=$(mktemp -d)
trap 'rm -rf "$KEYDIR"' EXIT
ssh-keygen -q -t ed25519 -N '' -C spoved-deploy -f "$KEYDIR/key"
echo "restrict,command=\"/usr/local/sbin/spoved-deploy\" $(cat "$KEYDIR/key.pub")" \
    | pct exec "$VMID" -- bash -c 'cat >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys'
# root logs in with this key only, and only to run the forced command
pct exec "$VMID" -- bash -euc '
    printf "PermitRootLogin forced-commands-only\nPasswordAuthentication no\n" > /etc/ssh/sshd_config.d/spoved-deploy.conf
    systemctl restart ssh
'
HOSTKEY=$(pct exec "$VMID" -- cat /etc/ssh/ssh_host_ed25519_key.pub | awk '{print $1" "$2}')

cat <<DONE

Container $VMID is ready at $IP. Give it a DHCP reservation so the address stays the same.
Add these secrets in InfiniteSpoved/spoved > Settings > Secrets and variables > Actions:

DEPLOY_LOCAL_HOST:
$IP

DEPLOY_LOCAL_KNOWN_HOSTS:
$IP $HOSTKEY

DEPLOY_LOCAL_SSH_KEY:
$(cat "$KEYDIR/key")

Then run Actions > Deploy local. After the first deploy, create your admin account:
  pct exec $VMID -- su git -c '/usr/local/bin/spoved admin user create --admin --username <name> --email <email> --random-password --config /etc/spoved/app.ini --work-path /var/lib/spoved'
The private key above is deleted from this host when the script exits.
DONE
