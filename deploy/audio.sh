#!/bin/bash
# PulseAudio for Chromium tab capture. Attendance still works if this no-ops.
set -u
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/0}"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR" 2>/dev/null || true
if ! command -v pulseaudio >/dev/null 2>&1; then
  exit 0
fi
if pulseaudio --check >/dev/null 2>&1; then
  exit 0
fi
pulseaudio --start --exit-idle-time=-1 --disallow-exit >/dev/null 2>&1 || true
exit 0
