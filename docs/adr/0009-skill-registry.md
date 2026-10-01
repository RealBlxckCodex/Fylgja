# ADR 0009: Skill-Registry statt zentralem Marketplace

**Status:** angenommen

**Kontext.** Skills sollen sich teilen lassen, ohne dass ein Fremder Code oder Anweisungen in einen Workspace schmuggelt.

**Entscheidung.** Eine Registry ist ein statisches JSON-Dokument (`skills.registries`), das irgendwo gehostet werden kann.
Jeder Eintrag trägt eine Ed25519-Signatur eines Publishers über Name, Version, Text, Dateien und Manifest
(`skills.Digest`, mit Domänen-Präfix). Nur Publisher aus `skills.trusted_publishers` zählen. Ohne Treffer, bei
falscher Signatur oder nachträglich verändertem Inhalt wird der Eintrag abgelehnt.

Die Installation (`POST /api/v1/skills/registry/install`) legt ausschließlich einen **Entwurf** mit `origin=imported` an.
Aktiviert wird er wie jeder andere Skill: Scanner, Step-up-Auth und lokale Signatur mit dem Workspace-Schlüssel. Der Scanner
läuft zusätzlich schon bei der Installation. Ein Publisher oder Registry-Betreiber kann also nichts aktivieren.

Publisher nutzen `fyctl skill keygen` und `fyctl skill sign`. Abrufe laufen über den netguard-Client, private Adressen
sind damit gesperrt.

**Nicht enthalten.** Kein Hosting, keine Suche, keine Bewertungen, keine Widerrufsliste für Publisher-Schlüssel
(Schlüssel aus der Konfiguration entfernen genügt, bereits installierte Skills bleiben dann aber bestehen).
