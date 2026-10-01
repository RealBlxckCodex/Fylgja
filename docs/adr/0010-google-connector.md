# ADR 0010: Google-Connector (Gmail, Kalender)

**Status:** angenommen

**Entscheidung.** Nativer OAuth-2.0-Connector (`internal/connectors/google`) statt eines MCP-Servers, damit Tokens, Policy
und Taint unter eigener Kontrolle bleiben.

- **Einwilligung:** `POST /dots/{id}/google/connect` (Step-up) liefert die Google-URL; Scopes hängen vom Modus ab
  (`read`: gmail.readonly, calendar.readonly; `read_write` zusätzlich gmail.send, calendar.events). Der State ist HMAC-signiert
  und bindet Workspace, Fylgja, Modus und Ablauf (10 min). Der Callback prüft, dass alle angefragten Scopes gewährt wurden.
- **Tokens:** Das Refresh-Token liegt im Vault (`oauth_token`, Domain-Bindung googleapis.com); Access-Tokens nur im Speicher.
- **Tools:** `gmail.search`, `gmail.read`, `calendar.list` (Klasse read, Ergebnis untrusted, taintet den Lauf),
  `gmail.send` (communicate, taint-sensitiv) und `calendar.create` (write_external, mit Empfängern für die Policy).
- **Doppelte Absicherung:** Der Handler prüft zusätzlich den `access_mode`; eine `read`-Verbindung kann nie schreiben,
  auch wenn eine Regel es erlauben würde. Empfänger und Betreff werden gegen Header-Injection geprüft.

**Grenzen.** Nur der Hauptkalender, kein Drive, keine Anhänge, kein Google-Workspace-Admin. Die Google-OAuth-App muss
vom Betreiber selbst in der Cloud Console angelegt werden; für sensible Scopes verlangt Google für Produktivnutzung eine
App-Verifizierung. Getestet nur gegen einen lokalen Fake (kein Live-Zugriff verfügbar).
