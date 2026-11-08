#!/bin/sh
set -e

if ! getent passwd dsui-agent >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin dsui-agent
fi

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi

cat <<'MSG'
dsui-agent installed.
1. Put the token from DSUI into /etc/dsui-agent/token (chmod 600).
2. Edit /etc/dsui-agent/agent.yaml (url and targets).
3. systemctl enable --now dsui-agent
4. dsui-agent check --config /etc/dsui-agent/agent.yaml --token-file /etc/dsui-agent/token
MSG
