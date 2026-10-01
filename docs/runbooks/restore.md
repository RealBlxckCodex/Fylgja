# Runbook: Restore aus Backup
1. Postgres per Basisbackup + WAL (PITR) wiederherstellen. 2. Master-Key bereitstellen. 3. `fylgja migrate`, dann `fylgja serve`.
4. `fyctl audit verify --db <url>` muss „kette intakt“ melden. Unterbrochene Runs werden automatisch fortgesetzt.
