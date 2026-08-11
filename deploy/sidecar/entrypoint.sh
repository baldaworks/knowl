#!/bin/sh
set -eu

config_dir="${KNOWL_CONFIG_DIR:-/etc}"

knowl --config-dir "$config_dir" init
exec knowl --config-dir "$config_dir" start
