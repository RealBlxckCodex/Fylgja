# ADR 0015: Aurora als Sprach-Engine

**Status:** angenommen

**Kontext.** Aurora (github.com/RealBlxckCodex/Aurora) ist eine selbst gehostete, CPU-first Audio-Engine mit OpenAI-kompatibler API
(`/v1/audio/speech`, `/v1/audio/transcriptions`, dazu `/v1/status`, `/v1/models`, `/v1/languages`). Sie passt zur Fylgja-Linie:
Sprache bleibt auf der eigenen Infrastruktur.

**Entscheidung.** Aurora wird wie jedes andere Modell über den Router angebunden, nicht als Sonderweg:

- Mit `voice.aurora.endpoint` (oder `FYLGJA_AURORA_URL`) legt Fylgja beim Start die logischen Modelle `stt` und `tts` (Privacy
  `self_hosted`) und die Deployments `aurora-stt` und `aurora-tts` an. Bereits selbst definierte Einträge gleichen Namens bleiben
  unverändert. Damit nutzen Web-Gespräch (ADR 0014) und die Sprachnachrichten von Telegram/Discord dieselbe Engine.
- Der API-Schlüssel (`FYLGJA_AURORA_API_KEY`) wird im Vault gespeichert und aus Logs entfernt.
- Stimme, Erkennungssprache und Tempo sind Workspace-Einstellungen (`PUT /voice/settings`, Rolle `manage`) und gehen als
  `voice`, `language` und `speed` an Aurora (über einen Kontext-Parameter, das Router-Interface bleibt gleich).
- `GET /voice/config` zeigt Erreichbarkeit, Version, geladene Modelle und Sprachen. Die Oberfläche (Einstellungen → Sprache)
  zeigt das und erlaubt Stimme wählen und Probehören.

**Grenzen.** Aurora hat keine API zum Laden oder Löschen von Modellen; das bleibt `aurora pull` und `aurora rm` auf dem
Aurora-Server. Die Stimmenliste kennt nur die in Aurora dokumentierten Modelle. Aurora liefert WAV oder MP3, kein Opus;
Telegram-Sprachantworten als OGG/Opus sind damit nicht abgedeckt. Streaming-TTS (SSE) von Aurora nutzen wir nicht, die
Satz-für-Satz-Ausgabe reicht für kurze Latenz. Das mitgelieferte `deploy/aurora/Dockerfile` ist ungetestet.
Gegen einen Mock mit Aurora-Antwortformat geprüft, nicht gegen eine echte Aurora-Instanz.
