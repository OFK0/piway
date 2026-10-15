#!/bin/sh
# Swaps a new piway binary in and restarts the service: the end of the
# development loop (docs/DEVELOPMENT.md). Runs on the Pi, locally for
# `make install` or over ssh for `make deploy-pi`.
#
#   swap-binary.sh NEW_BINARY [ADDR]
#
# NEW_BINARY must be on the same filesystem as ~/.local/bin: it is renamed
# over the old binary, which works while piway runs (copying over a running
# executable fails with "Text file busy").
set -eu

new=$1
addr=${2:-127.0.0.1:8080}
bin="$HOME/.local/bin/piway"

mkdir -p "$HOME/.local/bin"
chmod 755 "$new"
mv -f "$new" "$bin"
echo "installed $("$bin" -version)"

if ! systemctl --user cat piway >/dev/null 2>&1; then
	echo "no piway user service here yet: run 'piway setup' once (docs/INSTALL.md)"
	exit 0
fi
systemctl --user restart piway
i=0
until curl -fsS -o /dev/null "http://$addr/healthz" 2>/dev/null; do
	i=$((i + 1))
	if [ "$i" -ge 20 ]; then
		echo "piway did not answer at http://$addr/healthz within 20 s; see: journalctl --user -u piway -n 50" >&2
		exit 1
	fi
	sleep 1
done
echo "restarted; http://$addr/healthz answers"
