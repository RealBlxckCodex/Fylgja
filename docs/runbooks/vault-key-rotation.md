# Runbook: Vault-Key-Rotation
Neuen Master-Key erzeugen (`fylgja keygen`), als nächste `key_version` im Keyring hinterlegen; `Keyring.Rewrap` wrappt DEKs ohne Neu-Verschlüsselung der Daten. Master-Key getrennt vom DB-Backup aufbewahren – ohne ihn sind Secrets unlesbar.
