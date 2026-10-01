# ADR 0002 – Handgeschriebene pgx-Queries statt sqlc

Status: angenommen (vorläufig)

**Kontext.** Die Spec nennt `sqlc` für typsicheren DB-Zugriff. Für den Erstbau ändert sich das
Schema noch häufig; sqlc-Codegenerierung in jedem Schritt verlangsamt die Iteration.

**Entscheidung.** Queries werden direkt mit `pgx/v5` geschrieben, gekapselt in Store-Typen je
Package (`runtime.Store`, `coord.Store`, `memory.Service`, …). Jede Query ist durch
Integrationstests gegen echtes PostgreSQL + pgvector abgedeckt (`internal/store/testdb`).

**Konsequenzen.** Kein ORM, keine Laufzeitmagie – aber Tippfehler in SQL fallen erst im Test auf.
Die Migration auf sqlc ist mechanisch und kann paketweise erfolgen, sobald das Schema stabil ist.
