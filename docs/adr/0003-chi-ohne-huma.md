# ADR 0003 – chi ohne huma (OpenAPI-Generierung aufgeschoben)

Status: angenommen (vorläufig)

**Kontext.** Die Spec sieht `huma v2` für typisierte Handler und generierte OpenAPI vor.

**Entscheidung.** Die API nutzt `chi` mit expliziten Handlern, RFC-9457-Problem-Details,
Idempotency-Keys und Rollen-Middleware. Die OpenAPI-Beschreibung wird nachgezogen, sobald die
Endpunkte stabil sind (Migration: Handler-Signaturen auf huma-Operations umstellen).

**Konsequenzen.** Kein generiertes Schema für Fremdclients; `fyctl` und die Web-UI nutzen die API direkt.
