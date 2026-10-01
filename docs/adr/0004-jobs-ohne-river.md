# ADR 0004 – Hintergrundjobs ohne River

Status: angenommen (vorläufig)

**Kontext.** Die Spec nennt River als Postgres-basierte Job-Queue.

**Entscheidung.** Die Kernpfade brauchen River aktuell nicht:
- Runs sind über das Journal (`run_events`) event-sourced; `Engine.ResumeAll` nimmt unterbrochene
  Runs beim Start wieder auf – das ist der eigentliche Crash-Recovery-Mechanismus (Spec 8.8).
- Scheduler (Pulse, Routinen, Digest, Approval-Ablauf), Outbox-Zustellung, Koordinator-Ticks,
  Sandbox-Reaper und Nachtjobs laufen als Ticker-Loops; Mehrfachausführung wird per
  Compare-and-Set (`schedules.next_run_at`), `FOR UPDATE SKIP LOCKED` bzw. Advisory-Locks verhindert.

**Konsequenzen.** Weniger Abhängigkeiten; horizontale Skalierung mehrerer `worker`-Prozesse ist
für Runs noch nicht verteilt (Lanes sind prozesslokal). Für Multi-Worker-Betrieb wird die
Run-Zuteilung auf River (oder `SKIP LOCKED`-Claiming) umgestellt.
