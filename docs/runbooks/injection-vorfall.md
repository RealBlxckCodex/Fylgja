# Runbook: Injection-Vorfall
1. Activity → Run mit Badge `tainted`: Quelle (Event `taint`) und Tool-Calls im Journal ansehen.
2. Betroffene Memory-Einträge (`origin=untrusted`) löschen; Regeln mit `allow_when_tainted` prüfen.
3. Muster in `evals/corpus/injection.json` ergänzen (Regressionstest).
