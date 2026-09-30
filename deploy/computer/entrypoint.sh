#!/bin/bash
# Startet virtuellen Desktop (Xvfb), Fenstermanager, VNC → noVNC (nur im Sandbox-Netz) und computerd.
set -euo pipefail
mkdir -p /home/dot/workspace /home/dot/.browser /tmp/.X11-unix
Xvfb :1 -screen 0 1440x900x24 -nolisten tcp >/tmp/xvfb.log 2>&1 &
sleep 1
fluxbox >/tmp/fluxbox.log 2>&1 &
x11vnc -display :1 -rfbport 5900 -localhost -shared -forever -nopw -quiet >/tmp/x11vnc.log 2>&1 &
websockify --web /usr/share/novnc 6080 localhost:5900 >/tmp/websockify.log 2>&1 &
exec /usr/local/bin/computerd
