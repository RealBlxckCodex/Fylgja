-- +goose Up
-- Router, Flotte und Koordination (Spec 7.3).
CREATE TABLE model_catalog (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  logical_name text UNIQUE NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '{}',
  privacy_class text NOT NULL CHECK (privacy_class IN ('self_hosted','eu','any')),
  quality_rank smallint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE nodes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  provider text NOT NULL,
  provider_ref text NOT NULL DEFAULT '',
  gpu_model text NOT NULL DEFAULT '',
  gpu_count int NOT NULL DEFAULT 0,
  vram_gb int NOT NULL DEFAULT 0,
  region text NOT NULL DEFAULT '',
  state text NOT NULL CHECK (state IN ('provisioning','ready','draining','terminating','gone','failed')),
  tunnel_state text NOT NULL DEFAULT 'down',
  last_heartbeat timestamptz NULL,
  hourly_cost_micro_eur bigint NOT NULL DEFAULT 0,
  started_at timestamptz NOT NULL DEFAULT now(),
  terminated_at timestamptz NULL
);
CREATE TABLE deployments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  model_id uuid NOT NULL REFERENCES model_catalog(id) ON DELETE CASCADE,
  node_id uuid NULL REFERENCES nodes(id) ON DELETE SET NULL,
  provider text NOT NULL CHECK (provider IN ('runpod','local','external')),
  engine text NOT NULL CHECK (engine IN ('vllm','ollama','llamacpp','remote_api')),
  endpoint text NOT NULL,
  served_model text NOT NULL,
  api_kind text NOT NULL DEFAULT 'openai' CHECK (api_kind IN ('openai','anthropic')),
  secret_id uuid NULL REFERENCES vault_secrets(id),
  region text NOT NULL DEFAULT '',
  max_concurrency int NOT NULL DEFAULT 8,
  weight real NOT NULL DEFAULT 1.0,
  state text NOT NULL CHECK (state IN ('loading','ready','draining','failed','stopped')),
  cost_per_hour_micro_eur bigint NULL,
  price_in_micro_eur_per_mtok bigint NULL,
  price_out_micro_eur_per_mtok bigint NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE node_metrics (
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  ts timestamptz NOT NULL,
  gpu_util real, vram_used_mb int, vram_total_mb int, temp_c real, power_w real,
  running_reqs int, queued_reqs int, tokens_per_s real, kv_cache_util real,
  PRIMARY KEY (node_id, ts)
);
CREATE TABLE routing_decisions (
  id bigserial PRIMARY KEY,
  run_id uuid NULL,
  tier text NOT NULL DEFAULT '',
  logical_model text NOT NULL,
  deployment_id uuid NULL,
  reason text NOT NULL,
  queue_wait_ms int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE fleet_policies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  min_nodes int NOT NULL DEFAULT 0,
  max_nodes int NOT NULL DEFAULT 1,
  scale_up jsonb NOT NULL DEFAULT '{}',
  scale_down jsonb NOT NULL DEFAULT '{}',
  daily_budget_micro_eur bigint NOT NULL DEFAULT 0,
  hard boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT true
);

CREATE TABLE teams (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  lead_dot_id uuid NOT NULL REFERENCES dots(id),
  charter text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active'
);
CREATE TABLE team_members (
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  dot_id uuid NOT NULL REFERENCES dots(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('lead','member','reviewer')),
  PRIMARY KEY (team_id, dot_id)
);
CREATE TABLE worker_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  charter text NOT NULL DEFAULT '',
  tool_allowlist text[] NOT NULL DEFAULT '{}',
  model_tier text NOT NULL DEFAULT 'worker',
  sandbox_image text NOT NULL DEFAULT '',
  default_budget jsonb NOT NULL DEFAULT '{}',
  max_lifetime_s int NOT NULL DEFAULT 3600,
  egress_mode text NOT NULL DEFAULT 'allowlist'
);
CREATE TABLE work_graphs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id uuid NULL REFERENCES teams(id) ON DELETE CASCADE,
  lead_dot_id uuid NOT NULL REFERENCES dots(id),
  root_task_id uuid NULL REFERENCES tasks(id),
  title text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'planned',
  plan_version int NOT NULL DEFAULT 1,
  budget jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE workers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  template_id uuid NOT NULL REFERENCES worker_templates(id),
  parent_dot_id uuid NOT NULL REFERENCES dots(id),
  assignment_id uuid NULL,
  sandbox_id uuid NULL REFERENCES sandboxes(id),
  state text NOT NULL CHECK (state IN ('starting','idle','busy','done','failed','reaped')),
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz NULL
);
CREATE TABLE work_nodes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  graph_id uuid NOT NULL REFERENCES work_graphs(id) ON DELETE CASCADE,
  title text NOT NULL,
  goal text NOT NULL DEFAULT '',
  owner_kind text NOT NULL CHECK (owner_kind IN ('lead','member','worker','human')),
  owner_dot_id uuid NULL REFERENCES dots(id),
  worker_id uuid NULL REFERENCES workers(id),
  status text NOT NULL CHECK (status IN ('pending','ready','running','blocked','needs_review','done','failed','cancelled')),
  contract jsonb NOT NULL DEFAULT '{}',
  result_ref text NULL,
  result jsonb NULL,
  attempt int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE work_edges (
  graph_id uuid NOT NULL REFERENCES work_graphs(id) ON DELETE CASCADE,
  from_node uuid NOT NULL REFERENCES work_nodes(id) ON DELETE CASCADE,
  to_node uuid NOT NULL REFERENCES work_nodes(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('depends_on','reviews','feeds')),
  PRIMARY KEY (from_node, to_node, kind)
);
CREATE TABLE dot_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  from_dot uuid NOT NULL,
  to_dot uuid NOT NULL,
  graph_id uuid NULL REFERENCES work_graphs(id) ON DELETE CASCADE,
  node_id uuid NULL REFERENCES work_nodes(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('assign','question','answer','report','handoff','cancel')),
  body jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE blackboard (
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  key text NOT NULL,
  value jsonb NOT NULL,
  trust text NOT NULL DEFAULT 'owner',
  version int NOT NULL DEFAULT 1,
  updated_by uuid NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (team_id, key)
);
CREATE TABLE locks (
  resource text PRIMARY KEY,
  holder_node uuid NOT NULL,
  expires_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE locks, blackboard, dot_messages, work_edges, work_nodes, workers, work_graphs,
  worker_templates, team_members, teams, fleet_policies, routing_decisions, node_metrics,
  deployments, nodes, model_catalog;
