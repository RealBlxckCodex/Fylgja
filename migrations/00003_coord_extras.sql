-- +goose Up
-- Zusatzfelder für den Koordinator (Live-Ansicht, Retry-Historie).
ALTER TABLE work_nodes ADD COLUMN run_id uuid NULL;
ALTER TABLE work_nodes ADD COLUMN reason text NOT NULL DEFAULT '';
ALTER TABLE work_nodes ADD COLUMN notes text NOT NULL DEFAULT '';
ALTER TABLE work_nodes ADD COLUMN rationale text NOT NULL DEFAULT '';
ALTER TABLE work_nodes ADD COLUMN history jsonb NOT NULL DEFAULT '[]';
CREATE INDEX work_nodes_graph_idx ON work_nodes (graph_id);

-- +goose Down
ALTER TABLE work_nodes DROP COLUMN history, DROP COLUMN rationale, DROP COLUMN notes, DROP COLUMN reason, DROP COLUMN run_id;
