#!/bin/bash
# blender-thumb.sh — install the standalone blender-thumbnailer binary + register
# the .blend thumbnailer entry. Binary source: Ubuntu blender package's
# /usr/bin/blender-thumbnailer (self-contained ELF, GPL; reads the embedded preview
# block of Blender-saved .blend files — no Blender installation required).
# Tool-exported .blend files without an embedded preview produce no thumbnail
# (generic icon fallback) — that is expected, not a failure.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DST=/usr/local/bin/blender-thumbnailer
THUMB_DST=/usr/share/thumbnailers/blender.thumbnailer

if [ ! -x "$SCRIPT_DIR/bin/blender-thumbnailer" ]; then
  echo "FATAL: shipped binary missing at $SCRIPT_DIR/bin/blender-thumbnailer"
  exit 1
fi

changed=0
if ! cmp -s "$SCRIPT_DIR/bin/blender-thumbnailer" "$BIN_DST" 2>/dev/null; then
  sudo install -m 0755 "$SCRIPT_DIR/bin/blender-thumbnailer" "$BIN_DST"
  echo "blender-thumbnailer: installed to $BIN_DST"
  changed=1
else
  echo "blender-thumbnailer: already current at $BIN_DST"
fi

if ! cmp -s "$SCRIPT_DIR/blender.thumbnailer" "$THUMB_DST" 2>/dev/null; then
  sudo install -m 0644 "$SCRIPT_DIR/blender.thumbnailer" "$THUMB_DST"
  echo "blender.thumbnailer: registered at $THUMB_DST"
  changed=1
else
  echo "blender.thumbnailer: already registered"
fi

echo "blender-thumb step complete (changed=$changed)"
