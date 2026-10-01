# ADR 0011: Der eigene Computer der Fylgja

**Status:** angenommen

**Kontext.** Der eigene Computer ist das zentrale Merkmal: Jede Fylgja hat einen Desktop mit Browser, Shell und
persistentem Home, den sie bedient und den der Owner jederzeit ansehen und übernehmen kann.

**Entscheidungen.**

- **Isolation:** Provider `docker-gvisor` (Standard) oder `firecracker` (eigene microVM, KVM-Isolation). Beide bieten dieselbe
  `computerd`-API; das Home überlebt Schlafen und Aufwecken (Volume bzw. `home.ext4`). Details und Grenzen des
  Firecracker-Providers: `deploy/firecracker/README.md`.
- **Desktop-Steuerung:** `desktop.screenshot|click|drag|type|key|scroll|wait` über `xdotool` und ImageMagick. Argumente werden
  validiert (Koordinaten gegen die Bildschirmgröße, Tasten per Whitelist-Muster, Text hinter `--`), damit nichts als Option
  oder Shell-Kommando gelesen wird. Eingaben liefern automatisch ein frisches Bildschirmfoto.
- **Policy:** Screenshot ist `read`, Eingaben sind `compute` (wie `shell.run`). Bildschirminhalt gilt als nicht
  vertrauenswürdig und taintet den Lauf, denn eine Webseite oder ein Dokument kann Anweisungen enthalten.
- **Bilder im Kontext:** Tool-Ergebnisse können Bilder tragen (`Result.Images`, im Journal gespeichert). Die meisten APIs
  erlauben kein Bild in Tool-Nachrichten, daher folgt nach der Gruppe der Tool-Antworten eine User-Nachricht mit den Bildern.
  Nur die letzten zwei Bilder bleiben im Kontext, ältere werden durch einen Hinweis ersetzt. Das Modell muss Vision
  unterstützen; ohne Vision sind die Desktop-Tools nutzlos.
- **Sichtbarkeit:** Die Computer-Seite zeigt live den Bildschirm (noVNC, standardmäßig nur ansehen), einen Aktivitätsstrom
  der Computer-Tools, die Dateien und die Steuerung (Starten, Schlafen, Neu aufsetzen, Übernehmen). Übernehmen,
  Wake, Sleep, Reset und Datei-Downloads werden im Audit protokolliert. Das Ansehen weckt einen schlafenden Computer nicht auf.

**Grenzen.** Während der Owner übernommen hat, wird die Fylgja nicht pausiert. Das Journal speichert Screenshots mit
(bis 400 KB je Bild), lange Desktop-Läufe vergrößern es entsprechend. Nicht umgesetzt: Clipboard, Datei-Upload aus der
UI, Audio, Mehrfachmonitore, Speicher-Snapshots.
