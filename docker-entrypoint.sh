#!/bin/sh
set -e

# Secrets via Doppler: when a Doppler service token is present, re-exec this
# entrypoint under `doppler run` so EMAILDAWG_CONFIG_B64 + EMAILDAWG_PASSPHRASE
# come from Doppler (sliplane-services/prd_emaildawg) instead of raw Sliplane env
# vars. Backward compatible: with DOPPLER_TOKEN unset, the env-var path is used.
if [ -n "${DOPPLER_TOKEN:-}" ] && [ -z "${DOPPLER_INJECTED:-}" ]; then
  export DOPPLER_INJECTED=1
  exec doppler run --silent -- "$0" "$@"
fi

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
