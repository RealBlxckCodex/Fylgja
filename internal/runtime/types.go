// Package runtime ist die Agent-Runtime (Spec Kapitel 8): Lane Queue, Run Loop,
// Journal (event-sourced, write-ahead), Resume, Steer, Subagents, Budgets.
package runtime

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/policy"
)

type Kind string

const (
	KindChat          Kind = "chat"
	KindTaskStep      Kind = "task_step"
	KindPulse         Kind = "pulse"
	KindRoutine       Kind = "routine"
	KindSubagent      Kind = "subagent"
	KindReview        Kind = "review"
	KindConsolidation Kind = "consolidation"
	KindLearn         Kind = "learn"
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Waiting   Status = "waiting"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

func (s Status) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }

// Budget eines Runs.
type Budget struct {
	Tokens       int64 `json:"tokens,omitempty"`
	CostMicroEUR int64 `json:"cost_micro_eur,omitempty"`
	WallClockS   int   `json:"wall_clock_s,omitempty"`
}

// Input ist der Auftrag eines Runs.
type Input struct {
	Text          string          `json:"text"`
	Images        []string        `json:"images,omitempty"`
	Trust         string          `json:"trust"` // owner|member|untrusted|system
	Author        string          `json:"author,omitempty"`
	Channel       string          `json:"channel,omitempty"`
	Target        json.RawMessage `json:"target,omitempty"` // Zustellziel (Kanal)
	ToolAllowlist []string        `json:"tool_allowlist,omitempty"`
	Budget        Budget          `json:"budget,omitempty"`
	OutputSchema  json.RawMessage `json:"output_schema,omitempty"`
	MaxSteps      int             `json:"max_steps,omitempty"`
	Depth         int             `json:"depth,omitempty"`
	Privacy       string          `json:"privacy,omitempty"`
	NoTools       bool            `json:"no_tools,omitempty"`
	// Koordination: Knoten eines Work-Graphs, den dieser Run bearbeitet.
	Graph string `json:"graph,omitempty"`
	Node  string `json:"node,omitempty"`
}

// Run ist eine einzelne Ausführung des Agent-Loops.
type Run struct {
	ID             uuid.UUID    `json:"id"`
	DotID          uuid.UUID    `json:"dot_id"`
	TaskID         *uuid.UUID   `json:"task_id,omitempty"`
	ConversationID *uuid.UUID   `json:"conversation_id,omitempty"`
	ParentRunID    *uuid.UUID   `json:"parent_run_id,omitempty"`
	Kind           Kind         `json:"kind"`
	Scope          policy.Scope `json:"tool_scope"`
	Tainted        bool         `json:"tainted"`
	Status         Status       `json:"status"`
	Tier           string       `json:"model_tier"`
	Input          Input        `json:"input"`
	StartedAt      *time.Time   `json:"started_at,omitempty"`
	FinishedAt     *time.Time   `json:"finished_at,omitempty"`
	Error          string       `json:"error,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
}

// Event ist ein Journal-Eintrag.
type Event struct {
	RunID   uuid.UUID       `json:"run_id"`
	Seq     int             `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	At      time.Time       `json:"created_at"`
}

// Journal-Ereignistypen (7.1).
const (
	EvModelRequest      = "model_request"
	EvModelResponse     = "model_response"
	EvToolCall          = "tool_call"
	EvPolicy            = "policy"
	EvToolExec          = "tool_exec"
	EvToolResult        = "tool_result"
	EvApprovalRequested = "approval_requested"
	EvApprovalResolved  = "approval_resolved"
	EvSteer             = "steer"
	EvCheckpoint        = "checkpoint"
	EvMessageOut        = "message_out"
	EvTaint             = "taint"
	EvError             = "error"
	EvBudgetStop        = "budget_stop"
)

// Dot sind die für die Runtime relevanten Eigenschaften einer Fylgja.
type Dot struct {
	ID          uuid.UUID         `json:"id"`
	WorkspaceID uuid.UUID         `json:"workspace_id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	OwnerUserID *uuid.UUID        `json:"owner_user_id,omitempty"`
	OwnerName   string            `json:"owner_name"`
	Persona     string            `json:"persona"`
	Charter     string            `json:"charter"`
	Autonomy    int               `json:"autonomy_level"`
	Status      string            `json:"status"`
	PrivacyMode string            `json:"privacy_mode"`
	Timezone    string            `json:"timezone"`
	Locale      string            `json:"locale"`
	Tiers       map[string]string `json:"tiers"`
	QuietHours  json.RawMessage   `json:"quiet_hours"`
	PulseConfig json.RawMessage   `json:"pulse_config"`
	AvatarURL   string            `json:"avatar_url"`
	CreatedAt   time.Time         `json:"created_at"`
}

// StoredMessage ist eine Konversationsnachricht in der DB.
type StoredMessage struct {
	ID                uuid.UUID  `json:"id"`
	ConversationID    uuid.UUID  `json:"conversation_id"`
	RunID             *uuid.UUID `json:"run_id,omitempty"`
	Role              string     `json:"role"`
	Text              string     `json:"text"`
	Trust             string     `json:"trust"`
	Author            string     `json:"author,omitempty"`
	AuthorIdentityID  *uuid.UUID `json:"author_identity_id,omitempty"`
	PlatformMessageID string     `json:"platform_message_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

// UsageEvent bucht Kosten (20.2).
type UsageEvent struct {
	DotID        uuid.UUID
	RunID        uuid.UUID
	Model        string
	Tier         string
	Deployment   string
	TokensIn     int
	TokensOut    int
	TokensCached int
	CostMicroEUR int64
	LatencyMS    int
}
