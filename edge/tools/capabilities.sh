#!/usr/bin/env bash
#
# Push the custom capabilities to the SmartThings account.
#
#   cd edge && ./tools/capabilities.sh create   # first time on an account
#   cd edge && ./tools/capabilities.sh sync     # translations / record only
#   cd edge && ./tools/capabilities.sh sync --dry-run
#
# `create` runs capabilities:create + presentation:create + translations
# upsert for each name. Run it ONCE per account: a second run makes
# duplicates, and a capability a driver referenced cannot be deleted while
# it is referenced.
#
# `sync` runs capabilities:update + presentation:update + translations
# upsert. It only matters for translations and for the record: the hub
# caches a definition by id and a device screen keeps the FIRST
# presentation, so a change that must reach the phone needs a NEW id
# (new name in caps.lua, the JSON files and a new profile generation).
#
# After `create`, wait a few minutes before `smartthings edge:drivers:package`:
# the API answers "Invalid device profile specification" until the new
# capabilities have propagated (seen at ~6 minutes).
set -euo pipefail

cd "$(dirname "$0")/.."

NAMESPACE=numbersystem53811
NAMES=(ps5Power ps5Info ps5Wake ps5Bluetooth ps5RemotePlay ps5Version)
TAGS=(ko en)

mode="${1:-}"
dry=0
[ "${2:-}" = "--dry-run" ] && dry=1
if [ "$mode" != "create" ] && [ "$mode" != "sync" ]; then
  echo "usage: $0 create|sync [--dry-run]" >&2
  exit 2
fi

run() {
  if [ "$dry" -eq 1 ]; then
    printf '    smartthings'; printf ' %s' "$@"; printf '\n'
  else
    smartthings "$@"
  fi
}

for name in "${NAMES[@]}"; do
  id="${NAMESPACE}.$(echo "$name" | tr '[:upper:]' '[:lower:]')"
  echo "==> ${name} (${id})"
  for f in "capabilities/${name}.json" "capabilities/${name}.presentation.json" "capabilities/translations/${name}.ko.json" "capabilities/translations/${name}.en.json"; do
    [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
  done
  if [ "$mode" = "create" ]; then
    run capabilities:create -i "capabilities/${name}.json"
    run capabilities:presentation:create "$id" --capability-version 1 -i "capabilities/${name}.presentation.json"
  else
    run capabilities:update "$id" --capability-version 1 -i "capabilities/${name}.json"
    run capabilities:presentation:update "$id" --capability-version 1 -i "capabilities/${name}.presentation.json"
  fi
  for tag in "${TAGS[@]}"; do
    run capabilities:translations:upsert "$id" --capability-version 1 -i "capabilities/translations/${name}.${tag}.json"
  done
done
echo "done"
