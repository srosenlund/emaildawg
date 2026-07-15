#!/bin/sh
set -e

# --- Secrets injection ---------------------------------------------------------
# Preference order: BWS (homelab standard) -> Doppler (fallback) -> raw env vars.
# Whichever runtime is active re-execs this entrypoint with EMAILDAWG_CONFIG_B64 +
# EMAILDAWG_PASSPHRASE injected as env vars; a guard var prevents an infinite
# re-exec loop.

# Bitwarden Secrets Manager: when a machine-account token is present, re-exec under
# `bws run` so secrets come from the Homelab BWS project. This is the homelab
# standard and takes precedence over Doppler.
if [ -n "${BWS_ACCESS_TOKEN:-}" ] && [ -z "${SECRETS_INJECTED:-}" ]; then
  export SECRETS_INJECTED=1
  exec bws run -- "$0" "$@"
fi

# Doppler (legacy fallback): kept so the migration can flip via env without a
# rebuild. Only used when BWS_ACCESS_TOKEN is unset.
if [ -n "${DOPPLER_TOKEN:-}" ] && [ -z "${SECRETS_INJECTED:-}" ]; then
  export SECRETS_INJECTED=1
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
