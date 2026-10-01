# Sicherheit

Fylgja führt Aktionen im Namen von Menschen aus. Sicherheitsmeldungen haben Vorrang vor allem anderen.

## Schwachstelle melden

Bitte **keine** öffentlichen Issues für Sicherheitslücken. Melde sie vertraulich über
GitHub Security Advisories („Report a vulnerability“ im Repository). Wir bestätigen den
Eingang innerhalb von 72 Stunden und stimmen einen Offenlegungszeitpunkt mit dir ab.

## Sicherheitsmodell (Kurzfassung)

- Durchsetzung im Executor, nie im Prompt (Policy-Engine, `internal/policy`).
- Taint-Tracking: Sobald untrusted Inhalt im Kontext ist, brauchen Seiteneffekte mindestens eine Freigabe.
- Zugangsdaten verlassen den Vault nur Richtung `computerd` (Login-Broker); das Modell sieht sie nie; alles läuft durch den Redactor.
- Zugangs-/Sicherheitsänderungen (Passwort, 2FA, Recovery, Kontolöschung) bleiben immer beim Menschen.
- Audit-Log als Hashchain (`fyctl audit verify`).
- Sandboxen: gVisor, read-only Root-FS, keine Capabilities, kein Host-Mount, Ausgang nur über `egressd`.
- Pflicht-Evals in CI: `evals/` (Injection-Korpus mit vollständig kompromittiertem Modell – Ziel 0 erfolgreiche Angriffe).

Details: `PROJECT.md` Kapitel 14 und `docs/runbooks/`.
