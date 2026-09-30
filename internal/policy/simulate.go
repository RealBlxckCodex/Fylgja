package policy

// HistoricalAction ist eine vergangene Aktion inklusive ihrer damaligen Entscheidung.
type HistoricalAction struct {
	ID       string   `json:"id"`
	Action   Action   `json:"action"`
	Context  Context  `json:"context"`
	Previous Verdict  `json:"previous"`
}

// SimResult vergleicht alte und neue Entscheidung ("Was wäre passiert?", 14.4).
type SimResult struct {
	ID       string   `json:"id"`
	Tool     string   `json:"tool"`
	Previous Verdict  `json:"previous"`
	Now      Decision `json:"now"`
	Changed  bool     `json:"changed"`
}

// Simulate wertet ein (neues) Regelwerk gegen historische Aktionen aus.
func Simulate(rules RuleSet, hist []HistoricalAction) []SimResult {
	e := &Engine{Rules: rules}
	out := make([]SimResult, 0, len(hist))
	for _, h := range hist {
		d := e.Evaluate(h.Action, h.Context)
		out = append(out, SimResult{ID: h.ID, Tool: h.Action.Tool, Previous: h.Previous, Now: d, Changed: d.Verdict != h.Previous})
	}
	return out
}
