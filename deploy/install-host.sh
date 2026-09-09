#!/bin/sh
# Run as root from the repository root after staging /etc/click-the-button credentials.
set -eu
test "$(id -u)" -eq 0
test -f /etc/click-the-button/production.env
test -f /etc/click-the-button/google-client.json
command -v python3 >/dev/null
command -v sudo >/dev/null
if ! id click-the-button >/dev/null 2>&1; then
    useradd --system --home-dir /srv/click-the-button --shell /usr/sbin/nologin click-the-button
fi
install -d -m 0750 -o click-the-button -g click-the-button /srv/click-the-button /srv/click-the-button/releases /srv/click-the-button/shared /srv/click-the-button/shared/data /srv/click-the-button/backups
chown root:click-the-button /etc/click-the-button /etc/click-the-button/production.env /etc/click-the-button/google-client.json
chmod 0750 /etc/click-the-button
chmod 0640 /etc/click-the-button/production.env /etc/click-the-button/google-client.json
install -d -m 0755 /usr/local/lib/click-the-button
install -m 0644 deploy/release.py /usr/local/lib/click-the-button/release.py
for unit in deploy/*.service deploy/*.timer; do
    install -m 0644 "$unit" /etc/systemd/system/
done
sudoers=$(mktemp)
trap 'rm -f "$sudoers"' EXIT
printf '%s\n' 'click-the-button ALL=(root) NOPASSWD: /usr/bin/systemctl restart click-the-button.service' > "$sudoers"
visudo -cf "$sudoers"
install -m 0440 "$sudoers" /etc/sudoers.d/click-the-button
systemctl daemon-reload
if test -x /srv/click-the-button/current/server; then
    systemd-analyze verify /etc/systemd/system/click-the-button.service /etc/systemd/system/click-the-button-release.service /etc/systemd/system/click-the-button-backup.service
fi
systemctl enable click-the-button.service click-the-button-release.timer click-the-button-backup.timer
printf '%s\n' 'Host installed. Populate shared/data/legacy, install the first bundle, then start both timers.'
