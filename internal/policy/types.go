// Package policy ist die Durchsetzungsschicht für alle Tool-Aufrufe (Spec Kapitel 14).
//
// Grundsatz: Sicherheit wird hier im Executor erzwungen, nie im Prompt.
// Entscheidungshierarchie (14.3): deny-Regel > human-only > Scope (readonly/none)
// > Taint-Sperre > Custom Rules > Autonomie-Matrix.
package policy

import "time"

// Class ist die Aktionsklasse eines Tools (14.2).
type Class string

const (
	Read          Class = "read"
	WriteInternal Class = "write_internal"
	Compute       Class = "compute"
	WriteExternal Class = "write_external"
	Communicate   Class = "communicate"
	Spend         Class = "spend"
	Destructive   Class = "destructive"
	Credential    Class = "credential"
	Laptop        Class = "laptop"
)

// AllClasses in Reihenfolge steigenden Risikos.
var AllClasses = []Class{Read, WriteInternal, Compute, WriteExternal, Communicate, Destructive, Spend, Credential, Laptop}

func (c Class) Valid() bool {
	for _, x := range AllClasses {
		if x == c {
			return true
		}
	}
	return false
}

// SideEffect meldet Klassen, die außerhalb des eigenen Workspaces wirken.
func (c Class) SideEffect() bool {
	switch c {
	case WriteExternal, Communicate, Spend, Destructive, Laptop, Credential:
		return true
	}
	return false
}

// Scope ist der Tool-Scope eines Runs.
type Scope string

const (
	ScopeFull     Scope = "full"
	ScopeReadonly Scope = "readonly"
	ScopeNone     Scope = "none"
)

// Verdict ist das Ergebnis der Policy-Auswertung.
type Verdict string

const (
	Allow     Verdict = "allow"
	Ask       Verdict = "ask"
	Deny      Verdict = "deny"
	Review    Verdict = "review"     // Auto-Review entscheidet (14.6)
	HumanOnly Verdict = "human_only" // Aktion bleibt immer beim Menschen (Take-over)
)

// Effect einer Custom Rule.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectAsk   Effect = "ask"
	EffectDeny  Effect = "deny"
)

// Action beschreibt einen beabsichtigten Tool-Aufruf.
type Action struct {
	Tool           string         `json:"tool"`
	Class          Class          `json:"class"`
	Args           map[string]any `json:"args"`
	TargetDomain   string         `json:"target_domain,omitempty"`
	Recipients     []string       `json:"recipients,omitempty"`
	AmountMicroEUR int64          `json:"amount_micro_eur,omitempty"`
	EgressDomains  []string       `json:"egress_domains,omitempty"`
}

// Context beschreibt den Run, in dem die Aktion stattfindet.
type Context struct {
	DotID             string    `json:"dot"`
	Autonomy          int       `json:"autonomy"`
	Tainted           bool      `json:"tainted"`
	Trigger           string    `json:"trigger"` // chat|pulse|routine|task|subagent|worker
	Scope             Scope     `json:"scope"`
	Time              time.Time `json:"time"`
	Channel           string    `json:"channel"`
	UserRole          string    `json:"user_role"`
	ApprovedSameCount int       `json:"approved_same_count"`
	KnownContacts     []string  `json:"known_contacts"`
	// UsedEgressDomains: Domains, die im Run bereits benutzt wurden (Taint-Regel 2).
	UsedEgressDomains []string `json:"used_egress_domains"`
	// Allowlist des Tool-Scopes (Verträge/Worker). Leer = keine zusätzliche Einschränkung.
	ToolAllowlist []string `json:"tool_allowlist"`
}

// Decision ist die finale Policy-Entscheidung.
type Decision struct {
	Verdict           Verdict  `json:"verdict"`
	StepUp            bool     `json:"step_up"`            // Passkey-Bestätigung nötig
	RequiredApprovals int      `json:"required_approvals"` // 2 = Vier-Augen
	Reasons           []string `json:"reasons"`
	RuleID            string   `json:"rule_id,omitempty"`
	Risk              string   `json:"risk"`
}

func (d Decision) Allowed() bool { return d.Verdict == Allow }
