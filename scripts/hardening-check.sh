#!/usr/bin/env bash
# Checks the steps in docs/HARDENING.md and prints one line per check.
# Run it with sudo: most checks read root-only state. Exits 1 if a check
# fails. It changes nothing.
set -uo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo $0" >&2
  exit 2
fi
user=${SUDO_USER:-}

failed=0
ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; failed=1; }
warn() { printf '  warn  %s\n' "$1"; }
check() { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }

echo "1. SSH"
sshd=$(sshd -T 2>/dev/null)
setting() { awk -v k="$1" '$1 == k { print $2 }' <<<"$sshd"; }
if [ -z "$sshd" ]; then
  bad "cannot read sshd's settings (sshd -T)"
else
  [ "$(setting passwordauthentication)" = no ] && ok "password login off" || bad "password login is on"
  [ "$(setting kbdinteractiveauthentication)" = no ] && ok "keyboard-interactive login off" || bad "keyboard-interactive login is on"
  [ "$(setting permitrootlogin)" = no ] && ok "root login off" || bad "root login: $(setting permitrootlogin)"
  [ "$(setting x11forwarding)" = no ] && ok "X11 forwarding off" || warn "X11 forwarding is on"
fi
if [ -n "$user" ]; then
  home=$(getent passwd "$user" | cut -d: -f6)
  [ -s "$home/.ssh/authorized_keys" ] && ok "$user has an authorized key" || bad "$user has no ~/.ssh/authorized_keys"
fi

echo "2. Firewall"
if command -v ufw >/dev/null; then
  status=$(ufw status verbose)
  grep -q '^Status: active' <<<"$status" && ok "ufw active" || bad "ufw installed but inactive"
  grep -q 'deny (incoming)' <<<"$status" && ok "incoming denied by default" || bad "incoming not denied by default"
  grep -qE '^22/tcp .*LIMIT' <<<"$status" && ok "SSH rate-limited" || warn "no 'limit' rule for SSH"
  systemctl is-active --quiet ufw-safety.timer && warn "ufw-safety timer still pending: ufw turns off when it fires"
else
  bad "ufw not installed"
fi

echo "3. fail2ban"
if command -v fail2ban-client >/dev/null; then
  check "fail2ban running" "systemctl is-active --quiet fail2ban"
  check "sshd jail on" "fail2ban-client status sshd"
else
  bad "fail2ban not installed"
fi

echo "4. Automatic updates"
if [ -x /usr/bin/unattended-upgrade ]; then
  check "unattended upgrades enabled" "apt-config dump | grep -q 'APT::Periodic::Unattended-Upgrade \"1\"'"
  check "Raspberry Pi archive included" "apt-config dump | grep -q 'Origins-Pattern:: \"origin=Raspberry Pi Foundation'"
  check "upgrade timer active" "systemctl is-active --quiet apt-daily-upgrade.timer"
else
  bad "unattended-upgrades not installed"
fi

echo "5. Docker"
case $(systemctl is-active docker.service 2>/dev/null) in
  active) bad "root Docker daemon running" ;;
  inactive | failed) ok "root Docker daemon off" ;;
  *) warn "cannot tell whether the root Docker daemon runs" ;;
esac
if [ -n "$user" ]; then
  uid=$(id -u "$user")
  if sudo -u "$user" XDG_RUNTIME_DIR="/run/user/$uid" systemctl --user is-active --quiet docker 2>/dev/null; then
    ok "rootless Docker running"
  else
    warn "rootless Docker not running for $user"
  fi
fi

exit $failed
