package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ReviewInput bekommt der Reviewer (14.6). Bewusst OHNE rohen untrusted Inhalt.
type ReviewInput struct {
	TrustedInstructions []string       `json:"trusted_instructions"`
	Action              Action         `json:"action"`
	RulesSummary        string         `json:"rules_summary"`
	Autonomy            int            `json:"autonomy"`
	Tainted             bool           `json:"tainted"`
	ContextExcerpt      string         `json:"context_excerpt"`
	Charter             string         `json:"charter,omitempty"`
	Extra               map[string]any `json:"extra,omitempty"`
}

// ReviewOutput ist das schema-validierte Urteil.
type ReviewOutput struct {
	Verdict string   `json:"verdict"` // allow|needs_approval|deny
	Reasons []string `json:"reasons"`
	Risk    string   `json:"risk"` // low|medium|high
}

// Reviewer ist typischerweise ein LLM-Aufruf ohne Tools im Tier "reviewer".
type Reviewer interface {
	Review(ctx context.Context, in ReviewInput) (json.RawMessage, error)
}

// ParseReview validiert die Ausgabe streng gegen das Schema.
func ParseReview(raw []byte) (ReviewOutput, error) {
	var out ReviewOutput
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("review: ungültiges json: %w", err)
	}
	switch out.Verdict {
	case "allow", "needs_approval", "deny":
	default:
		return out, fmt.Errorf("review: verdict %q ungültig", out.Verdict)
	}
	switch out.Risk {
	case "low", "medium", "high":
	default:
		return out, fmt.Errorf("review: risk %q ungültig", out.Risk)
	}
	return out, nil
}

// RunReview führt das Auto-Review fail-closed aus: Timeout, Fehler oder ungültige
// Ausgabe → Ask. Ein "allow" mit Risiko "high" wird ebenfalls zu Ask.
func RunReview(ctx context.Context, r Reviewer, in ReviewInput, timeout time.Duration) (Verdict, ReviewOutput) {
	if r == nil {
		return Ask, ReviewOutput{Verdict: "needs_approval", Reasons: []string{"kein reviewer konfiguriert"}, Risk: "medium"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := r.Review(ctx, in)
	if err != nil {
		return Ask, ReviewOutput{Verdict: "needs_approval", Reasons: []string{"review fehlgeschlagen: " + err.Error()}, Risk: "medium"}
	}
	out, err := ParseReview(extractJSON(raw))
	if err != nil {
		return Ask, ReviewOutput{Verdict: "needs_approval", Reasons: []string{err.Error()}, Risk: "medium"}
	}
	switch out.Verdict {
	case "allow":
		if out.Risk == "high" {
			return Ask, out
		}
		return Allow, out
	case "deny":
		return Deny, out
	}
	return Ask, out
}
