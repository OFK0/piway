#!/usr/bin/env bash
# Prints the licenses of the Go code linked into cmd/piway: the standard
# library and runtime, then every module dependency. Used by the Makefile to
# build THIRD_PARTY_NOTICES.txt. Fails if a module has no license file.
set -euo pipefail

rule() { printf '%*s\n' 78 '' | tr ' ' '='; }

mods=$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/piway | sort -u)
count=$(( $(printf '%s\n' "$mods" | grep -c . || true) + 1 ))
echo "Server (Go): $count components"
echo

rule
echo "Go $(go env GOVERSION | sed 's/^go//') standard library and runtime (BSD-3-Clause)"
rule
echo
cat "$(go env GOROOT)/LICENSE"
echo

printf '%s\n' "$mods" | while read -r path version dir; do
  [ -n "$path" ] || continue
  file=$(find "$dir" -maxdepth 1 -type f | grep -iE '/(licen[cs]e|copying)(\.(md|txt))?$' | sort | head -n1 || true)
  if [ -z "$file" ]; then
    echo "go-notices: no license file for $path $version" >&2
    exit 1
  fi
  rule
  echo "$path $version"
  rule
  echo
  cat "$file"
  echo
done
