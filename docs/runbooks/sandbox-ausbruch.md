# Runbook: Sandbox-Ausbruch-Verdacht
1. Notbremse (hart stoppen). 2. Sandbox-Container der betroffenen Fylgja stoppen und Volume sichern (Forensik).
3. egressd-Logs der Quell-IP prüfen. 4. Image und Runtime (`runsc`) aktualisieren, Secrets der Fylgja rotieren.
