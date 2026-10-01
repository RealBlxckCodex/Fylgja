#!/bin/bash
# Einmalige Host-Einrichtung für Fylgja-microVMs: Netzregeln.
# Gäste dürfen NUR den Control Plane (Port 7070/6080 werden vom Host aus erreicht, nicht umgekehrt)
# und den Egress-Proxy auf der Host-Adresse ihres tap-Netzes (Port 3128) ansprechen. Alles andere wird verworfen.
set -euo pipefail
SUBNET="${SUBNET:-172.31.0.0/16}"
PROXY_PORT="${PROXY_PORT:-3128}"

[ -e /dev/kvm ] || { echo "/dev/kvm fehlt: dieser Host kann keine microVMs starten" >&2; exit 1; }
sysctl -w net.ipv4.ip_forward=0 >/dev/null   # kein Routing der Gäste ins Netz

nft -f - <<NFT
table inet fylgja_vm {
  chain input {
    type filter hook input priority 0; policy accept;
    ip saddr $SUBNET tcp dport $PROXY_PORT accept
    ip saddr $SUBNET ct state established,related accept
    ip saddr $SUBNET drop
  }
  chain forward {
    type filter hook forward priority 0; policy accept;
    ip saddr $SUBNET drop
    ip daddr $SUBNET drop
  }
}
NFT
echo "nft-Regeln gesetzt. Egress-Proxy (egressd) muss auf den tap-Host-Adressen (172.31.x.1:$PROXY_PORT) oder 0.0.0.0:$PROXY_PORT lauschen."
