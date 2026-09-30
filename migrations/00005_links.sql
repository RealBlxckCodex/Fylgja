-- +goose Up
-- Laptop-Link (Spec 13.9): outbound-only Agent auf Nutzergeräten.
CREATE TABLE links (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash bytea NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '{}',
  last_seen timestamptz NULL,
  revoked_at timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE links;
