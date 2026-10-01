# ADR 0014: Live-Gespräch im Web-UI

**Status:** angenommen

**Entscheidung.** Das Chat-Fenster hat einen Mikrofon-Knopf. Ist er an, läuft ein Gespräch ohne Tastendruck:

1. **Zuhören:** Der Browser nimmt das Mikrofon auf (Echo- und Rauschunterdrückung), misst den Pegel und erkennt Sprechbeginn
   und Sprechpause (1,1 s) selbst. Die Schwelle passt sich dem Grundrauschen an.
2. **Verstehen:** Die Aufnahme geht an `POST /dots/{id}/voice/transcribe` (max. 10 MB, `audio/*`). Der Server nutzt das logische
   Modell `stt` (OpenAI-kompatibel, `/audio/transcriptions`). Der Text wird danach wie eine getippte Nachricht gesendet.
   Die Transkription selbst löst nichts aus.
3. **Antworten:** Während die Antwort streamt, wird sie satzweise über `POST /dots/{id}/voice/speak` (Modell `tts`,
   `/audio/speech`, MP3, max. 1500 Zeichen) gesprochen. Markdown wird bereinigt, Code-Blöcke werden nicht vorgelesen.
4. **Unterbrechen:** Wer während der Ausgabe spricht, stoppt die Wiedergabe und den laufenden Lauf; die neue Äußerung wird aufgenommen.

Ohne konfigurierte Modelle `stt`/`tts` fehlt der Knopf (`GET /voice/status`); ohne `tts` kommen Antworten nur als Text.

**Sicherheit.** Sprache ist nur ein anderer Eingabeweg: Sie läuft durch dieselbe Policy, dieselben Freigaben und dasselbe
Taint-Verhalten wie Text. Freigaben mit Step-up lassen sich nicht per Sprache erteilen. Die Modelle sind auf self-hosted
beschränkt, Audio verlässt also die eigene Infrastruktur nicht. Anfragen sind pro Nutzer ratenbegrenzt; die
Berechtigungsrichtlinie erlaubt das Mikrofon nur für die eigene Seite.

**Grenzen.** Kein durchgehender Audio-Stream (Äußerung für Äußerung, Latenz = Sprechpause + STT + Modell + erste TTS-Sprache).
Die Sprechpausen-Erkennung ist eine einfache Pegelschwelle; in lauter Umgebung oder ohne Kopfhörer kann die eigene
Ausgabe als Unterbrechung gelten. Nicht umgesetzt: Telefon/SIP, Sprecherkennung, mehrere Sprecher, Stimmauswahl im UI.
Mit einem Fake-Mikrofon und einem Mock-Server geprüft, nicht mit echten STT-/TTS-Modellen.
