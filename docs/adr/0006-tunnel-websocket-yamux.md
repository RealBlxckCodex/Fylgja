# ADR 0006 – Node- und Link-Tunnel: WebSocket + yamux, Token-Authentifizierung

Status: angenommen

**Kontext.** Spec 16.9 verlangt einen ausgehenden WSS/mTLS-Tunnel ohne öffentliche Inference-Ports.

**Entscheidung.** Nodes (`fylgja-node`) und Laptops (`fylgja-link`) bauen eine WebSocket-Verbindung
zum Control Plane auf; darüber läuft `yamux`. Der Control Plane öffnet Streams zum Node, die dieser
an die lokale OpenAI-API (vLLM/Ollama) bzw. den lokalen Link-Server weiterreicht. Authentifizierung:
einmaliges, zufälliges Token pro Node/Link (nur SHA-256-Hash gespeichert) über TLS (Traefik).

**mTLS (optional, `fleet.require_mtls`).** Die Node-CA wird per HKDF deterministisch aus dem Master-Key
abgeleitet (Ed25519, feste Gültigkeit), braucht also keine Ablage und ist über Replikas identisch. Ein Node holt
sich mit seinem Token über `POST /api/v1/node/enroll` ein 24-h-Client-Zertifikat für eine lokal erzeugte CSR
(CN wird serverseitig auf die Node-ID gesetzt, nur `clientAuth`), erneuert es nach 2/3 der Laufzeit und zeigt es
beim Tunnelaufbau vor. Der Control Plane prüft Kette, Gültigkeit und CN = Node-ID zusätzlich zum Token.
Voraussetzung: Der Control Plane terminiert TLS selbst (`tls.cert_file/key_file`). Hinter einem Proxy, der TLS
beendet, ist das nicht möglich; dort bleibt es beim Token. Eine Widerrufsliste gibt es nicht: Revoke/Terminate
macht das Token ungültig, das Zertifikat läuft spätestens nach 24 h aus.
