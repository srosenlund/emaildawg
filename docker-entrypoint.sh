#!/bin/sh
set -e

# Sliplane (and any env-driven host) delivers the bbctl-generated Beeper config
# base64-encoded as the EMAILDAWG_CONFIG_B64 secret. Decode it into the data
# volume on startup and run the bridge against it. If a config already exists in
# the volume (e.g. mounted directly), it is left untouched unless the env var is
# set.
CONFIG_PATH="${EMAILDAWG_CONFIG_PATH:-/home/nonroot/app/data/config.yaml}"

if [ -n "${EMAILDAWG_CONFIG_B64:-}" ]; then
  echo "$EMAILDAWG_CONFIG_B64" | base64 -d > "$CONFIG_PATH"
fi

if [ ! -f "$CONFIG_PATH" ]; then
  echo "FATAL: no config at $CONFIG_PATH and EMAILDAWG_CONFIG_B64 is unset" >&2
  exit 1
fi

exec /usr/bin/emaildawg --config "$CONFIG_PATH"
