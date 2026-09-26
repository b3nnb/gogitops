#!/bin/bash
# install.sh — idempotent install of gnome-sushi + f3d from the Ubuntu repos.
# sushi = GNOME's spacebar quick-previewer (image/video/audio/text/pdf/font/html).
# f3d  = instant 3D viewer + ships /usr/share/thumbnailers/f3d-plugin-*.thumbnailer
#        (STL/OBJ/GLB/PLY/FBX/3DS/VTK... grid thumbnails via VTK native/assimp/occt plugins).
set -u

PKGS=(gnome-sushi f3d)
INSTALL=()
for p in "${PKGS[@]}"; do
  if dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed'; then
    echo "$p: installed ($(dpkg-query -W -f='${Version}' "$p")) -> converged"
  else
    echo "$p: missing -> will install"
    INSTALL+=("$p")
  fi
done

if [ ${#INSTALL[@]} -eq 0 ]; then
  echo "all preview packages present"
  exit 0
fi

sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${INSTALL[@]}"
for p in "${INSTALL[@]}"; do
  echo "$p: now $(dpkg-query -W -f='${Status}' "$p" 2>/dev/null || echo '?')"
done
echo "installed: ${INSTALL[*]}"
