# ADR 0005 – Telegram über die Bot-API direkt

Status: angenommen

Statt `go-telegram/bot` spricht der Adapter die Bot-API direkt über HTTP (`internal/channels/telegram`).
Gründe: wenige benötigte Methoden, volle Kontrolle über Retry/429-Handling, Token-Redaction in Fehlern
und einfache Tests gegen einen Mock-Server. Discord nutzt wie spezifiziert `discordgo`.
