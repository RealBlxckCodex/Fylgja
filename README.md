# Fylgja

Selbst gehostete Plattform für **persistente, proaktive KI-Agents („Fylgjur“)** mit eigenem Computer,
Langzeitgedächtnis, strikter Kontrolle und Bedienung über **Discord, Telegram und Web-UI**.
Eigener Inference-Router für eine RunPod-/lokale GPU-Flotte und ein Koordinationssystem (Lead + Teammates + Worker).

Spezifikation: `PROJECT.md` (v1.1). Abweichungen: `docs/adr/`. Betrieb: `docs/runbooks/`.

## Schnellstart (Entwicklung)
```bash
# Postgres 17 + pgvector, dann:
export FYLGJA_DATABASE_URL=postgres://… FYLGJA_MASTER_KEY=$(go run ./cmd/fylgja keygen)
(cd web && npm ci && npm run build)
go run ./cmd/fylgja serve -config deploy/fylgja.example.yaml   # http://localhost:8080 → Setup
```
Produktion: `deploy/docker-compose.yml` (Traefik, pgvector, egressd).

## Binaries
`fylgja` (Control Plane) · `fyctl` (CLI) · `computerd` (Sandbox-Daemon) · `egressd` (Egress-Proxy) · `fylgja-node` (GPU-Node-Agent) · `fylgja-link` (Laptop-Link)

## Umsetzungsstand
Implementiert und getestet (Unit + Integration gegen PostgreSQL/pgvector, Race-Detector, Injection-Evals):
Vault/Audit-Hashchain, Policy-Engine (CEL, Autonomie-Matrix, Taint, Auto-Review, Approvals, Vier-Augen),
Runtime (Lane Queue, Journal/Resume, Context-Assembler, Subagents, Quarantäne), Memory (hybride Suche,
Trust-Gating, Export), Inference-Router (Privacy-Routing, Prioritäten, Breaker, Fallback), Fleet Manager
(RunPod, Autoscaling, Orphan-Reconciler, Node-Tunnel), Koordination (Work-Graph, Verträge, Worker, Locks, Bus),
Telegram/Discord, Pulse, Lernschleife, Skills (Signatur/Scanner), Sandbox (Docker+gVisor), MCP-Client, Web-UI.

**Nicht live gegen echte Dienste verifiziert** (kein Zugriff in der Build-Umgebung): RunPod-API, Discord-Gateway,
Telegram-Server, Docker/gVisor-Sandboxen, Playwright-MCP-Browser, WebAuthn im Browser. Diese Teile sind per
Mock-/Unit-Tests abgedeckt; vor Produktivbetrieb bitte einmal Ende-zu-Ende prüfen.
Nicht umgesetzt: Live-Voice (Phase 12), Telegram Mini App.
Der Firecracker-Provider ist nur gegen einen Fake der Firecracker-API getestet (kein KVM in der Entwicklungsumgebung).
Der Google- und der Microsoft-365-Connector (Mail, Kalender) sind nur gegen einen lokalen Fake-Server getestet, nicht gegen die echten Dienste.

MIT-Lizenz.
