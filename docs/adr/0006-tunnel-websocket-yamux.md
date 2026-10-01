# ADR 0006 – Node- und Link-Tunnel: WebSocket + yamux, Token-Authentifizierung

Status: angenommen

**Kontext.** Spec 16.9 verlangt einen ausgehenden WSS/mTLS-Tunnel ohne öffentliche Inference-Ports.

**Entscheidung.** Nodes (`fylgja-node`) und Laptops (`fylgja-link`) bauen eine WebSocket-Verbindung
zum Control Plane auf; darüber läuft `yamux`. Der Control Plane öffnet Streams zum Node, die dieser
an die lokale OpenAI-API (vLLM/Ollama) bzw. den lokalen Link-Server weiterreicht. Authentifizierung:
einmaliges, zufälliges Token pro Node/Link (nur SHA-256-Hash gespeichert) über TLS (Traefik).

**Abweichung.** Kurzlebige Client-Zertifikate (mTLS) pro Node sind noch nicht umgesetzt; das Token
wird bei Terminate/Revoke sofort ungültig. mTLS kann ergänzend am Reverse-Proxy erzwungen werden.
