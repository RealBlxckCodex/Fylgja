# Runbook: Node ausgefallen
1. Flotte → Nodes: Status `failed`/Heartbeat alt? Der Router hat den Circuit Breaker bereits geöffnet, Anfragen laufen über andere Deployments (Privacy-Klassen bleiben gewahrt).
2. Autoscaler ersetzt den Node, falls `min_nodes` unterschritten ist. Sonst: `fyctl fleet drain <id>` und `POST /fleet/pools/<pool>/provision`.
3. Verwaister Pod beim Provider? Der Reconciler terminiert getaggte Pods minütlich selbst (Alarm `orphan_pod`).
