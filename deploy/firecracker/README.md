# Firecracker-Sandboxen

Jede Fylgja bekommt eine eigene microVM: eigener Kernel, KVM-Isolation, ein schreibgeschütztes Root-Image und ein
persistentes Home-Image (`home.ext4`), das Schlafen und Aufwecken überlebt. Das Computer-Userland ist dasselbe wie im
Docker-Provider (Desktop, Browser, `computerd`).

**Nicht gegen echtes Firecracker getestet.** Die Entwicklungsumgebung hatte kein `/dev/kvm`. Der Provider ist gegen einen
Fake der Firecracker-API getestet (Aufrufreihenfolge, Adressvergabe, Schlafen und Aufwecken); die Shell-Skripte hier sind
auf Syntax geprüft, aber nie in einer echten VM gelaufen. Rechne beim ersten Einsatz mit Nacharbeit am Init-Skript.

## Voraussetzungen

- Linux-Host mit `/dev/kvm`, `firecracker` (v1.7 oder neuer), `iproute2`, `e2fsprogs`, `nftables`
- Der Control Plane läuft auf diesem Host (Rolle `all` oder `worker`) mit Zugriff auf `/dev/kvm` und `CAP_NET_ADMIN`
  (tap-Geräte anlegen)
- Ein Linux-Kernel (`vmlinux`) mit `CONFIG_IP_PNP`, ext4 und virtio-Blockgeräten, z. B. aus den Firecracker-CI-Artefakten

## Einrichtung

```
sudo ./host-setup.sh                      # nft-Regeln: Gäste erreichen nur den Egress-Proxy
./build-rootfs.sh /var/lib/fylgja/rootfs.ext4
```

```yaml
sandbox:
  provider: firecracker
  firecracker:
    kernel: /var/lib/fylgja/vmlinux
    rootfs: /var/lib/fylgja/rootfs.ext4
    data_dir: /var/lib/fylgja/vms
    home_mb: 8192            # sparse, wächst mit der Nutzung
```

## Netz und Egress

Jede VM bekommt ein eigenes /30 aus `172.31.0.0/16` (Host `.1`, Gast `.2`) auf einem eigenen tap-Gerät. Es gibt kein
Routing ins Netz (`ip_forward=0`). Der Gast erreicht nur den Egress-Proxy auf der Host-Adresse (`:3128`), der
Control Plane erreicht `computerd` (`:7070`) und die Live-Ansicht (`:6080`) des Gastes. Im Modus `offline` bekommt der
Gast keinen Proxy.

## Schlafen und Aufwecken

Schlafen fährt die VM sauber herunter (Ctrl-Alt-Del, danach der Init-Prozess). Aufwecken bootet neu und hängt dasselbe
Home-Image wieder ein; offene Programme sind danach weg, Dateien bleiben. Snapshots des Arbeitsspeichers sind nicht
umgesetzt.

## Grenzen

- Keinen `jailer`: Der Firecracker-Prozess läuft mit den Rechten des Control Planes. Für Produktion `jailer` mit eigenem
  Benutzer, cgroup und chroot ergänzen.
- Der Token für `computerd` steht auf der Kernel-Kommandozeile und ist damit im Gast unter `/proc/cmdline` lesbar. Das
  ist nur das Token für die eigene Sandbox.
- Das Root-Image ist ein Export des Docker-Images; Sicherheitsupdates heißen: Image neu bauen und VMs neu starten.
