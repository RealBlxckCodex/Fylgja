-- +goose Up
-- Fylgja Kern-Schema (Spec Kapitel 7.1). IDs sind UUIDv7 aus der Anwendung;
-- gen_random_uuid() ist nur Fallback.
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS citext;

-- Mandanten & Nutzer -------------------------------------------------------
CREATE TABLE workspaces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  settings jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email citext UNIQUE NOT NULL,
  display_name text NOT NULL DEFAULT '',
  pw_hash text NULL,
  locale text NOT NULL DEFAULT 'de',
  timezone text NOT NULL DEFAULT 'Europe/Berlin',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE memberships (
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('owner','admin','member','viewer','auditor')),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE passkeys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE api_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  hash bytea NOT NULL UNIQUE,
  scopes text[] NOT NULL DEFAULT '{}',
  expires_at timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
  id_hash bytea PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  step_up_until timestamptz NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Kanal-Identitäten & Pairing ---------------------------------------------
CREATE TABLE channel_identities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform text NOT NULL CHECK (platform IN ('discord','telegram','web')),
  platform_user_id text NOT NULL,
  display text NOT NULL DEFAULT '',
  verified_at timestamptz NULL,
  UNIQUE (platform, platform_user_id)
);

-- Fylgjur ------------------------------------------------------------------
CREATE TABLE model_profiles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  tiers jsonb NOT NULL DEFAULT '{}',   -- tier -> logisches Modell
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE dots (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('personal','specialist')),
  owner_user_id uuid NULL REFERENCES users(id),
  persona text NOT NULL DEFAULT '',
  charter text NOT NULL DEFAULT '',
  autonomy_level smallint NOT NULL DEFAULT 1 CHECK (autonomy_level BETWEEN 0 AND 3),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','archived')),
  privacy_mode text NOT NULL DEFAULT 'self_hosted_only' CHECK (privacy_mode IN ('self_hosted_only','eu_only','any')),
  model_profile_id uuid NULL REFERENCES model_profiles(id),
  pulse_config jsonb NOT NULL DEFAULT '{}',
  quiet_hours jsonb NOT NULL DEFAULT '{}',
  avatar_url text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pairing_codes (
  code text PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  used_at timestamptz NULL
);
CREATE TABLE dot_channels (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  platform text NOT NULL,
  config_enc bytea NULL,
  status text NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE dot_grants (
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  grant_name text NOT NULL,           -- chat | approve:<klasse> | view_computer | takeover
  PRIMARY KEY (dot_id, user_id, grant_name)
);

-- Konversationen & Nachrichten --------------------------------------------
CREATE TABLE tasks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  title text NOT NULL,
  goal text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('queued','planning','running','waiting_approval','waiting_input','blocked','done','failed','cancelled')),
  priority smallint NOT NULL DEFAULT 0,
  plan jsonb NOT NULL DEFAULT '{}',
  due_at timestamptz NULL,
  budget_tokens bigint NULL,
  parent_task_id uuid NULL REFERENCES tasks(id),
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE conversations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  platform text NOT NULL,
  platform_chat_id text NOT NULL,
  platform_thread_id text NOT NULL DEFAULT '',
  kind text NOT NULL CHECK (kind IN ('dm','group','thread','web','task')),
  task_id uuid NULL REFERENCES tasks(id),
  summary text NOT NULL DEFAULT '',
  summary_upto_msg uuid NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (dot_id, platform, platform_chat_id, platform_thread_id)
);
CREATE TABLE runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  task_id uuid NULL REFERENCES tasks(id),
  conversation_id uuid NULL REFERENCES conversations(id),
  parent_run_id uuid NULL REFERENCES runs(id),
  kind text NOT NULL CHECK (kind IN ('chat','task_step','pulse','routine','subagent','review','consolidation','learn')),
  tool_scope text NOT NULL CHECK (tool_scope IN ('full','readonly','none')),
  tainted boolean NOT NULL DEFAULT false,
  status text NOT NULL CHECK (status IN ('queued','running','waiting','succeeded','failed','cancelled')),
  model_tier text NOT NULL DEFAULT 'worker',
  input jsonb NOT NULL DEFAULT '{}',
  started_at timestamptz NULL,
  finished_at timestamptz NULL,
  error text NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runs_status_idx ON runs (status) WHERE status IN ('queued','running','waiting');
CREATE INDEX runs_dot_idx ON runs (dot_id, created_at DESC);
CREATE TABLE messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  run_id uuid NULL REFERENCES runs(id) ON DELETE SET NULL,
  role text NOT NULL CHECK (role IN ('user','assistant','system','tool')),
  author_identity_id uuid NULL REFERENCES channel_identities(id),
  content jsonb NOT NULL,
  platform_message_id text NOT NULL DEFAULT '',
  reply_to uuid NULL,
  trust text NOT NULL CHECK (trust IN ('owner','member','untrusted','system')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_conv_idx ON messages (conversation_id, created_at);
-- messages.trust ist unveränderlich (Spec 7.2).
-- +goose StatementBegin
CREATE FUNCTION forbid_trust_update() RETURNS trigger AS $$
BEGIN
  IF NEW.trust IS DISTINCT FROM OLD.trust THEN
    RAISE EXCEPTION 'messages.trust ist unveränderlich';
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER messages_trust_immutable BEFORE UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION forbid_trust_update();

CREATE TABLE run_events (
  run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  seq int NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, seq)
);
-- run_events ist append-only (Spec 7.2).
-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION '% ist append-only', TG_TABLE_NAME;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER run_events_append_only BEFORE UPDATE ON run_events FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Approvals, Regeln, Proposals --------------------------------------------
CREATE TABLE approvals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NULL REFERENCES runs(id) ON DELETE SET NULL,
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  tool text NOT NULL,
  class text NOT NULL,
  args_redacted jsonb NOT NULL DEFAULT '{}',
  preview jsonb NOT NULL DEFAULT '{}',
  risk text NOT NULL DEFAULT 'medium',
  reason text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('pending','approved','denied','expired','cancelled')),
  approver_group text NULL,
  required_approvals smallint NOT NULL DEFAULT 1,
  approvals_given jsonb NOT NULL DEFAULT '[]',
  resolved_by uuid NULL REFERENCES users(id),
  resolved_via text NULL,
  resolved_at timestamptz NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approvals_pending_idx ON approvals (dot_id) WHERE status = 'pending';
CREATE TABLE rules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  dot_id uuid NULL REFERENCES dots(id) ON DELETE CASCADE,
  name text NOT NULL,
  expr text NOT NULL,
  effect text NOT NULL CHECK (effect IN ('allow','ask','deny')),
  priority int NOT NULL DEFAULT 0,
  enabled boolean NOT NULL DEFAULT true,
  allow_when_tainted boolean NOT NULL DEFAULT false,
  four_eyes boolean NOT NULL DEFAULT false,
  version int NOT NULL DEFAULT 1,
  created_by uuid NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  type text NOT NULL CHECK (type IN ('rule','preference','skill','memory_edit','action','pulse_item')),
  payload jsonb NOT NULL DEFAULT '{}',
  evidence jsonb NOT NULL DEFAULT '[]',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','rejected','expired')),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Connectors & Secrets -----------------------------------------------------
CREATE TABLE vault_secrets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  dot_id uuid NULL REFERENCES dots(id) ON DELETE CASCADE,
  type text NOT NULL CHECK (type IN ('password','totp','api_key','oauth_token','ssh_key','cookie_jar')),
  label text NOT NULL,
  ciphertext bytea NOT NULL,
  wrapped_dek bytea NOT NULL,
  key_version int NOT NULL,
  meta jsonb NOT NULL DEFAULT '{}',      -- u. a. allowed_domains (Domain-Bindung)
  created_at timestamptz NOT NULL DEFAULT now(),
  rotated_at timestamptz NULL
);
CREATE TABLE connectors (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  kind text NOT NULL,
  name text NOT NULL,
  config jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'active'
);
CREATE TABLE connections (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  connector_id uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  account_label text NOT NULL DEFAULT '',
  scopes text[] NOT NULL DEFAULT '{}',
  access_mode text NOT NULL CHECK (access_mode IN ('read','read_write')),
  secret_id uuid NULL REFERENCES vault_secrets(id),
  status text NOT NULL DEFAULT 'active'
);

-- Memory -------------------------------------------------------------------
CREATE TABLE memories (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  tier text NOT NULL CHECK (tier IN ('core','semantic','episodic','procedural','note')),
  content text NOT NULL,
  embedding vector(1024) NULL,
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
  importance real NOT NULL DEFAULT 0.5,
  sensitivity text NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal','private','secret-adjacent')),
  origin text NOT NULL DEFAULT 'owner' CHECK (origin IN ('owner','system','member','untrusted')),
  source jsonb NOT NULL DEFAULT '{}',
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to timestamptz NULL,
  superseded_by uuid NULL REFERENCES memories(id) ON DELETE SET NULL,
  pinned boolean NOT NULL DEFAULT false,
  access_count int NOT NULL DEFAULT 0,
  last_accessed timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops);
CREATE INDEX memories_tsv_idx ON memories USING gin (tsv);
CREATE INDEX memories_dot_idx ON memories (dot_id, tier);
CREATE TABLE entities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  kind text NOT NULL,
  name text NOT NULL,
  aliases text[] NOT NULL DEFAULT '{}',
  attributes jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE entity_mentions (
  entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  memory_id uuid NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  PRIMARY KEY (entity_id, memory_id)
);
CREATE TABLE skills (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  dot_id uuid NULL REFERENCES dots(id) ON DELETE CASCADE,
  name text NOT NULL,
  version int NOT NULL DEFAULT 1,
  description text NOT NULL DEFAULT '',
  body_md text NOT NULL DEFAULT '',
  files jsonb NOT NULL DEFAULT '{}',
  manifest jsonb NOT NULL DEFAULT '{}',
  signature bytea NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','disabled')),
  origin text NOT NULL CHECK (origin IN ('builtin','user','dot_authored','imported')),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Proaktivität -------------------------------------------------------------
CREATE TABLE signals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  source text NOT NULL,
  kind text NOT NULL,
  dedup_key text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}',
  score real NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'new',
  seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX signals_dedup_idx ON signals (dot_id, source, dedup_key) WHERE dedup_key <> '';
CREATE TABLE schedules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('pulse','routine')),
  cron text NOT NULL,
  prompt text NOT NULL DEFAULT '',
  tool_scope text NOT NULL DEFAULT 'readonly',
  target jsonb NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  next_run_at timestamptz NULL
);

-- Sandbox ------------------------------------------------------------------
CREATE TABLE sandbox_hosts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  endpoint text NOT NULL,
  provider text NOT NULL,
  capacity jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'ready'
);
CREATE TABLE sandboxes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid UNIQUE NULL REFERENCES dots(id) ON DELETE CASCADE,
  host_id uuid NULL REFERENCES sandbox_hosts(id),
  image text NOT NULL,
  state text NOT NULL CHECK (state IN ('creating','running','sleeping','stopped','error')),
  volume_ref text NOT NULL DEFAULT '',
  resources jsonb NOT NULL DEFAULT '{}',
  last_active_at timestamptz NULL
);
CREATE TABLE artifacts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  task_id uuid NULL REFERENCES tasks(id),
  name text NOT NULL,
  mime text NOT NULL,
  size bigint NOT NULL,
  storage_ref text NOT NULL,
  sha256 bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Feedback, Kosten, Audit --------------------------------------------------
CREATE TABLE feedback (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  run_id uuid NULL REFERENCES runs(id) ON DELETE SET NULL,
  message_id uuid NULL REFERENCES messages(id) ON DELETE SET NULL,
  kind text NOT NULL CHECK (kind IN ('thumb_up','thumb_down','edit','correction','approval_denied','approval_always')),
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE usage_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dot_id uuid NULL REFERENCES dots(id) ON DELETE SET NULL,
  run_id uuid NULL,
  model text NOT NULL,
  tier text NOT NULL,
  deployment text NOT NULL DEFAULT '',
  tokens_in int NOT NULL DEFAULT 0,
  tokens_out int NOT NULL DEFAULT 0,
  tokens_cached int NOT NULL DEFAULT 0,
  cost_micro_eur bigint NOT NULL DEFAULT 0,
  latency_ms int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_events_time_idx ON usage_events (created_at);
CREATE TABLE budgets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  scope text NOT NULL CHECK (scope IN ('workspace','team','dot','task','graph')),
  scope_id uuid NOT NULL,
  period text NOT NULL CHECK (period IN ('day','month','total')),
  limit_micro_eur bigint NOT NULL,
  hard boolean NOT NULL DEFAULT false,
  UNIQUE (scope, scope_id, period)
);
CREATE TABLE audit_log (
  id bigserial PRIMARY KEY,
  workspace_id uuid NOT NULL,
  actor text NOT NULL,
  action text NOT NULL,
  target text NOT NULL DEFAULT '',
  detail jsonb NOT NULL DEFAULT '{}',
  prev_hash bytea NULL,
  hash bytea NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Kanal-Zustellung (Outbox, Dedup) -----------------------------------------
CREATE TABLE inbound_dedup (
  platform text NOT NULL,
  chat_id text NOT NULL,
  message_id text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (platform, chat_id, message_id)
);
CREATE TABLE outbox (
  id bigserial PRIMARY KEY,
  dot_id uuid NULL,
  platform text NOT NULL,
  target jsonb NOT NULL,
  message jsonb NOT NULL,
  lane text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed')),
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  platform_ref text NOT NULL DEFAULT '',
  error text NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_pending_idx ON outbox (next_attempt_at) WHERE status = 'pending';
-- Event-Fan-out für SSE (Retention 10 min, Spec 19.4)
CREATE TABLE events (
  id bigserial PRIMARY KEY,
  topic text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_topic_idx ON events (topic, id);

-- Row-Level-Security als Defense-in-Depth (Spec 7): greift, sobald eine
-- Verbindung fylgja.workspace_id setzt (request-gebundene Verbindungen).
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['dots','rules','vault_secrets','connectors','skills','model_profiles'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY ws_isolation ON %I USING (
      coalesce(current_setting('fylgja.workspace_id', true), '') = ''
      OR workspace_id = current_setting('fylgja.workspace_id', true)::uuid)$p$, t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
