# ADR 0013: Telegram Mini App

**Status:** angenommen

**Entscheidung.** Unter `/tg` liegt eine kleine Ansicht für Telegram: Freigaben (freigeben oder ablehnen) und Chat mit
der Fylgja. Der Menü-Knopf des Bots öffnet sie (`setChatMenuButton`, wird beim Start des Bots gesetzt).

- **Anmeldung:** Die App schickt die signierten `initData` an `POST /api/v1/auth/telegram/webapp`. Der Server prüft die
  HMAC-Signatur mit dem Bot-Token (nach Telegrams Vorschrift), das Alter (höchstens 1 Stunde, kein Datum in der Zukunft)
  und verlangt eine bereits gekoppelte, verifizierte Telegram-Identität (`/pair CODE`). Fremde und ungekoppelte Nutzer
  bekommen dieselbe 401-Antwort. Die Sitzung gilt 8 Stunden.
- **Freigaben mit Step-up** lassen sich in der Mini App nicht erteilen; dort steht der Hinweis, das Web-UI zu öffnen.
  Telegram-Zugang ersetzt also keine Passkey-Bestätigung.
- **Cookie:** `SameSite=None; Secure`, weil `web.telegram.org` die App in einem iframe einbettet. Browser, die Drittanbieter-
  Cookies sperren, können dort scheitern; die nativen Telegram-Apps sind nicht betroffen.
- **Header:** Nur für `/tg` erlaubt die CSP Telegrams Skript (`https://telegram.org`) und das Einbetten durch
  `web.telegram.org`. Überall sonst bleibt `frame-ancestors 'none'`.
- **Voraussetzung:** Bot aktiv und `base_url` mit `https://` (Telegram verlangt https). Sonst ist die Mini App aus und der
  Endpunkt antwortet 404.

**Nicht getestet** gegen echtes Telegram. Geprüft sind die Signaturprüfung und der Login-Pfad (Go-Tests) sowie die Oberfläche
mit einem Telegram-Stub im Browser.
