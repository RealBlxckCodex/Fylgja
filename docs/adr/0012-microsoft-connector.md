# ADR 0012: Microsoft-365-Connector (Outlook, Kalender)

**Status:** angenommen

Gleiche Bauweise wie der Google-Connector (ADR 0010): OAuth 2.0 mit Authorization Code, signierter State, Verbindung pro
Fylgja, Zugriffsmodus `read` oder `read_write`, Mail- und Kalenderinhalte sind nicht vertrauenswürdig, Senden und
Termine anlegen laufen durch die Policy.

**Unterschiede.**

- API ist Microsoft Graph. Tools: `outlook.search`, `outlook.read`, `outlook.send`, `outlook.calendar_list`, `outlook.calendar_create`.
  Scopes: `Mail.Read`, `Calendars.Read`, bei Schreibzugriff `Mail.Send` und `Calendars.ReadWrite`, dazu `offline_access` und `User.Read`.
- **Refresh-Token-Rotation:** Microsoft stellt bei jeder Erneuerung ein neues Refresh-Token aus und entwertet das alte. Das neue
  wird sofort im Vault ersetzt; schlägt das fehl, scheitert der Aufruf, statt mit einem toten Token weiterzumachen.
- Der Tenant ist konfigurierbar (`microsoft.tenant`: `common`, `organizations` oder eine Tenant-ID). Für Firmenkonten
  braucht ein Entra-Administrator oft die Zustimmung zu den Berechtigungen.
- Termine werden in UTC angelegt. Graph versendet Einladungen selbst.

**Grenzen.** Nur das Hauptpostfach und der Standardkalender, keine geteilten Postfächer, keine Anhänge, kein Teams/SharePoint/OneDrive.
Getestet nur gegen einen lokalen Fake-Server, nicht gegen Microsoft.
