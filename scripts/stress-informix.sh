#!/usr/bin/env bash
set -euo pipefail
umask 077

mode=${1:-both}
case "$mode" in
  stress|fuzz|both|soak|gc) ;;
  *) echo 'usage: scripts/stress-informix.sh [stress|fuzz|both|soak|gc]' >&2; exit 2 ;;
esac
cd "$(dirname "$0")/.."
log_dir=${IFX_STRESS_LOG_DIR:-$(mktemp -d /tmp/ifxodbc-stress.XXXXXX)}
mkdir -p "$log_dir"
echo "Logs: $log_dir"

# Pass credentials and container settings through make variables in the environment.
# -e lets those settings override the defaults in Makefile.
run() {
  local label=$1
  shift
  make -e "$@" 2>&1 | tee "$log_dir/$label.log"
}

case "$mode" in
  stress) run stress stress-ifx ;;
  fuzz) run fuzz fuzz-ifx ;;
  both) run stress stress-ifx; run fuzz fuzz-ifx ;;
  soak) run soak stress-ifx IFX_STRESS_DURATION="${IFX_STRESS_DURATION:-30m}" IFX_TEST_TIMEOUT="${IFX_TEST_TIMEOUT:-40m}" ;;
  gc) run gc stress-ifx IFX_GOGC=1 IFX_GOMEMLIMIT="${IFX_GOMEMLIMIT:-200MiB}" ;;
esac
