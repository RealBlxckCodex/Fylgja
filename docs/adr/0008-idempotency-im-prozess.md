# ADR 0008 – Idempotency-Keys im Prozessspeicher

Status: angenommen (vorläufig)

Schreibende API-Endpunkte akzeptieren `Idempotency-Key`; Antworten werden 24 h im Prozessspeicher
gehalten. Für Einzelinstanz-Betrieb (Referenz-Deployment) ausreichend. Bei mehreren API-Prozessen
wird auf eine Postgres-Tabelle umgestellt.
