package policy

import (
	"fmt"
	"slices"
	"strings"
)

// Engine entscheidet über Aktionen. Sie ist zustandslos und deterministisch.
type Engine struct {
	Rules RuleSet
}

func restrictiveness(e Effect) int {
	switch e {
	case EffectDeny:
		return 3
	case EffectAsk:
		return 2
	default:
		return 1
	}
}

// Evaluate berechnet die Entscheidung für eine Aktion.
func (e *Engine) Evaluate(a Action, c Context) Decision {
	d := Decision{RequiredApprovals: 1, Risk: riskOf(a.Class, c.Tainted)}
	if !a.Class.Valid() {
		// Unbekannte Klassen werden konservativ als write_external behandelt (12.1).
		d.Reasons = append(d.Reasons, fmt.Sprintf("unbekannte klasse %q → write_external", a.Class))
		a.Class = WriteExternal
	}
	level := min(max(c.Autonomy, 0), 3)
	act := Activation(a, c)

	// Regeln auswerten (nur solche, die für diese Fylgja gelten).
	var matched []*Compiled
	for _, r := range e.Rules {
		if r.DotID != "" && r.DotID != c.DotID {
			continue
		}
		ok, err := r.Match(act)
		if err != nil {
			if r.Effect == EffectDeny {
				// Fail-closed: eine deny-Regel, die nicht ausgewertet werden kann, erzwingt Nachfrage.
				matched = append(matched, &Compiled{Rule: Rule{ID: r.ID, Name: r.Name + " (auswertungsfehler)", Effect: EffectAsk, Priority: r.Priority}})
			}
			continue
		}
		if ok {
			matched = append(matched, r)
		}
	}

	// 1. deny-Regel gewinnt immer.
	for _, r := range matched {
		if r.Effect == EffectDeny {
			d.Verdict, d.RuleID = Deny, r.ID
			d.Reasons = append(d.Reasons, "deny-regel: "+r.Name)
			return d
		}
	}
	// 2. human-only.
	if a.Class == Credential {
		d.Verdict = HumanOnly
		d.Reasons = append(d.Reasons, "credential-aktionen bleiben immer beim menschen")
		return d
	}
	// 3. Scope (hart, unabhängig vom Prompt).
	switch c.Scope {
	case ScopeNone:
		d.Verdict = Deny
		d.Reasons = append(d.Reasons, "run hat keinen tool-scope")
		return d
	case ScopeReadonly:
		if a.Class != Read && a.Class != WriteInternal {
			d.Verdict = Deny
			d.Reasons = append(d.Reasons, fmt.Sprintf("readonly-scope: klasse %s nicht aufrufbar", a.Class))
			return d
		}
	}
	if len(c.ToolAllowlist) > 0 && !toolAllowed(a.Tool, c.ToolAllowlist) {
		d.Verdict = Deny
		d.Reasons = append(d.Reasons, "tool nicht im vertrags-scope")
		return d
	}

	// Stärkste passende Nicht-deny-Regel bestimmen (höchste Priorität, bei Gleichstand restriktiver).
	var rule *Compiled
	for _, r := range matched {
		if rule == nil || r.Priority > rule.Priority ||
			(r.Priority == rule.Priority && restrictiveness(r.Effect) > restrictiveness(rule.Effect)) {
			rule = r
		}
	}

	// 4. Taint-Sperre.
	taintFloor, egressFloor := false, false
	if c.Tainted {
		switch a.Class {
		case WriteExternal, Communicate, Spend, Destructive, Laptop:
			taintFloor = true
		}
		for _, dom := range a.EgressDomains {
			if !slices.Contains(c.UsedEgressDomains, strings.ToLower(dom)) {
				egressFloor = true
				d.Reasons = append(d.Reasons, "getainteter run: neue egress-domain "+dom)
			}
		}
	}

	// 5. Custom Rules, 6. Matrix.
	base := Matrix[a.Class][level]
	verdict := base
	if rule != nil {
		d.RuleID = rule.ID
		switch rule.Effect {
		case EffectAsk:
			verdict = Ask
			d.Reasons = append(d.Reasons, "regel: "+rule.Name)
			if rule.FourEyes {
				d.RequiredApprovals = 2
			}
		case EffectAllow:
			switch {
			case a.Class == Spend && level < 3:
				d.Reasons = append(d.Reasons, "allow-regel für spend greift erst ab L3")
			case c.Tainted && !rule.AllowWhenTainted && taintFloor:
				d.Reasons = append(d.Reasons, "allow-regel ohne allow_when_tainted im getainteten run ignoriert")
			default:
				verdict = Allow
				d.Reasons = append(d.Reasons, "regel: "+rule.Name)
				if rule.AllowWhenTainted {
					taintFloor = false
				}
			}
		}
	}
	if (taintFloor || egressFloor) && (verdict == Allow || verdict == Review) {
		verdict = Ask
		d.Reasons = append(d.Reasons, "kontext enthält untrusted inhalt")
	}
	if verdict == base && rule == nil {
		d.Reasons = append(d.Reasons, fmt.Sprintf("matrix L%d/%s", level, a.Class))
	}
	d.Verdict = verdict
	if a.Class == Spend && verdict == Ask {
		d.StepUp = true
	}
	return d
}

func toolAllowed(tool string, allow []string) bool {
	for _, p := range allow {
		if p == tool || (strings.HasSuffix(p, ".*") && strings.HasPrefix(tool, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// ScopeSubset prüft "Rechte schrumpfen nur" (17.3): child ⊆ parent.
// Eine leere Parent-Liste bedeutet "alles erlaubt".
func ScopeSubset(child, parent []string) bool {
	if len(parent) == 0 {
		return true
	}
	if len(child) == 0 {
		return false // leeres Kind hieße "alles" – mehr als der Parent
	}
	for _, t := range child {
		if strings.HasSuffix(t, ".*") {
			if !slices.Contains(parent, t) {
				return false
			}
			continue
		}
		if !toolAllowed(t, parent) {
			return false
		}
	}
	return true
}

// IntersectScope ist der Scope eines Subagents: Eltern ∩ Auftrag (8.10).
func IntersectScope(parent, requested Scope) Scope {
	rank := map[Scope]int{ScopeNone: 0, ScopeReadonly: 1, ScopeFull: 2}
	if rank[requested] < rank[parent] {
		return requested
	}
	return parent
}
