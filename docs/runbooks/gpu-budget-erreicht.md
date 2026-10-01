# Runbook: GPU-Budget erreicht
Soft-Limit (80 %): kein Scale-up, Owner-Alarm. Hard-Limit: Drain aller Nodes des Pools.
Fortsetzen: Budget in der Scaling-Policy anheben (Step-up) oder Tag abwarten. Leerlaufanteil in Flotte → Kapazität & Kosten prüfen; ggf. `min_nodes` senken.
