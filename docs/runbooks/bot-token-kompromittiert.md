# Runbook: Bot-Token kompromittiert
1. Token beim Anbieter (BotFather / Discord Developer Portal) sofort neu erzeugen.
2. `FYLGJA_TELEGRAM_TOKEN` / `FYLGJA_DISCORD_TOKEN` ersetzen, Dienst neu starten.
3. Audit prüfen (`/audit`, Aktion `channel.pair`), unbekannte gepairte Identitäten entfernen.
