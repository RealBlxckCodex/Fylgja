-- +goose Up
-- Persistenz des Fleet Managers (Nodes überleben Neustarts; Token nur als Hash).
ALTER TABLE nodes ADD COLUMN pool text NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN token_hash bytea NULL;
ALTER TABLE nodes ADD COLUMN node_deployments jsonb NOT NULL DEFAULT '[]';
ALTER TABLE nodes ADD COLUMN metrics jsonb NOT NULL DEFAULT '{}';
-- System-Workspace für globale Secrets (z. B. API-Keys externer Modell-Deployments).
INSERT INTO workspaces (id, name) VALUES ('00000000-0000-0000-0000-000000000000', '_system') ON CONFLICT DO NOTHING;

-- +goose Down
ALTER TABLE nodes DROP COLUMN metrics, DROP COLUMN node_deployments, DROP COLUMN token_hash, DROP COLUMN pool;
