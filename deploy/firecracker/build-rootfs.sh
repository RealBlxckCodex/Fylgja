#!/bin/bash
# Baut das schreibgeschützte Root-Image für die Fylgja-microVMs aus dem Computer-Container.
#   ./build-rootfs.sh [ausgabe.ext4] [image-tag]
# Braucht Docker, mkfs.ext4 (e2fsprogs) und Root-Rechte für das Entpacken.
set -euo pipefail
OUT="${1:-rootfs.ext4}"
TAG="${2:-fylgja-computer:firecracker}"
SIZE_MB="${ROOTFS_MB:-3072}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

docker build -f "$ROOT/deploy/computer/Dockerfile" -t "$TAG" "$ROOT"
CID="$(docker create "$TAG")"
STAGE="$(mktemp -d)"
trap 'docker rm -f "$CID" >/dev/null 2>&1 || true; rm -rf "$STAGE"' EXIT
docker export "$CID" | tar -C "$STAGE" -xf -

# Init, das den Gast aus der Kernel-Kommandozeile konfiguriert.
install -D -m 0755 "$HERE/fylgja-init" "$STAGE/sbin/fylgja-init"
mkdir -p "$STAGE"/{proc,sys,dev,run,tmp,home/dot,overlay}

truncate -s "${SIZE_MB}M" "$OUT"
mkfs.ext4 -q -F -L fylgja-root -d "$STAGE" "$OUT"
echo "fertig: $OUT ($(du -h "$OUT" | cut -f1) belegt)"
