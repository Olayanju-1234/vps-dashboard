#!/bin/sh
# Installs or upgrades vps-dashboard. Run as root on the server:
#   sudo sh install.sh <binary> <config.json> <unit dir>
# Idempotent: creates the two system users once, keeps the previous binary as
# .prev, validates the config before restarting anything.
set -eu

BIN=${1:?binary}
CFG=${2:?config.json}
UNITS=${3:?folder with the .service files}
DEST=/opt/vps-dashboard

if ! getent group vpsdash >/dev/null; then groupadd --system vpsdash; fi
if ! id vpsdash >/dev/null 2>&1; then
  useradd --system --gid vpsdash --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin vpsdash
fi
if ! id vpsdash-probe >/dev/null 2>&1; then
  useradd --system --gid vpsdash --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin vpsdash-probe
fi

# Root owns the binary: the probe runs it with a capability, so nobody else may replace it.
install -d -m 755 -o root -g root "$DEST"
if [ -f "$DEST/vps-dashboard" ]; then cp -p "$DEST/vps-dashboard" "$DEST/vps-dashboard.prev"; fi
install -m 755 -o root -g root "$BIN" "$DEST/vps-dashboard.new"
"$DEST/vps-dashboard.new" check-config --config "$CFG"
mv "$DEST/vps-dashboard.new" "$DEST/vps-dashboard"

install -d -m 750 -o root -g vpsdash /etc/vps-dashboard
install -m 640 -o root -g vpsdash "$CFG" /etc/vps-dashboard/config.json
install -m 644 -o root -g root "$UNITS/vps-dashboard.service" "$UNITS/vps-dashboard-probe.service" /etc/systemd/system/

systemctl daemon-reload
systemctl enable vps-dashboard-probe.service vps-dashboard.service >/dev/null 2>&1
systemctl restart vps-dashboard-probe.service
sleep 2
systemctl restart vps-dashboard.service
sleep 2
systemctl is-active vps-dashboard-probe.service vps-dashboard.service
