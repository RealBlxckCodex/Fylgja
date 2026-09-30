package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/ext"
)

// Rule ist eine Custom Rule in CEL (14.4).
type Rule struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DotID            string `json:"dot_id,omitempty"` // leer = workspace-weit
	Expr             string `json:"expr"`
	Effect           Effect `json:"effect"`
	Priority         int    `json:"priority"`
	Enabled          bool   `json:"enabled"`
	AllowWhenTainted bool   `json:"allow_when_tainted"`
	FourEyes         bool   `json:"four_eyes"`
	Version          int    `json:"version"`
}

var (
	envOnce sync.Once
	celEnv  *cel.Env
	envErr  error
)

func env() (*cel.Env, error) {
	envOnce.Do(func() {
		dyn := cel.MapType(cel.StringType, cel.DynType)
		celEnv, envErr = cel.NewEnv(
			cel.Variable("action", dyn),
			cel.Variable("ctx", dyn),
			cel.Variable("user", dyn),
			cel.Variable("history", dyn),
			cel.Variable("contacts", dyn),
			ext.Strings(),
		)
	})
	return celEnv, envErr
}

// Compiled ist eine übersetzte Regel.
type Compiled struct {
	Rule
	prg cel.Program
}

// Compile prüft und übersetzt eine Regel. Der Ausdruck muss bool liefern.
func Compile(r Rule) (*Compiled, error) {
	switch r.Effect {
	case EffectAllow, EffectAsk, EffectDeny:
	default:
		return nil, fmt.Errorf("regel %q: effect %q ungültig", r.Name, r.Effect)
	}
	e, err := env()
	if err != nil {
		return nil, err
	}
	ast, iss := e.Compile(r.Expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("regel %q: %w", r.Name, iss.Err())
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("regel %q: ausdruck muss bool liefern, nicht %s", r.Name, ast.OutputType())
	}
	prg, err := e.Program(ast, cel.CostLimit(100000), cel.EvalOptions(cel.OptOptimize))
	if err != nil {
		return nil, fmt.Errorf("regel %q: %w", r.Name, err)
	}
	return &Compiled{Rule: r, prg: prg}, nil
}

// Activation baut die CEL-Variablen aus Aktion und Kontext.
func Activation(a Action, c Context) map[string]any {
	domain := a.TargetDomain
	recips := make([]any, 0, len(a.Recipients))
	for _, r := range a.Recipients {
		recips = append(recips, strings.ToLower(r))
		if domain == "" {
			if i := strings.LastIndex(r, "@"); i >= 0 {
				domain = strings.ToLower(r[i+1:])
			}
		}
	}
	args := a.Args
	if args == nil {
		args = map[string]any{}
	}
	known := make([]any, 0, len(c.KnownContacts))
	for _, k := range c.KnownContacts {
		known = append(known, strings.ToLower(k))
	}
	return map[string]any{
		"action": map[string]any{
			"tool":             a.Tool,
			"class":            string(a.Class),
			"args":             args,
			"amount_micro_eur": a.AmountMicroEUR,
			"target":           map[string]any{"domain": domain, "recipients": recips},
		},
		"ctx": map[string]any{
			"dot": c.DotID, "autonomy": int64(c.Autonomy), "tainted": c.Tainted,
			"trigger": c.Trigger, "time": c.Time, "channel": c.Channel, "scope": string(c.Scope),
		},
		"user":     map[string]any{"role": c.UserRole},
		"history":  map[string]any{"approved_same_count": int64(c.ApprovedSameCount)},
		"contacts": map[string]any{"known": known},
	}
}

// Match wertet die Regel aus. Fehler (z. B. fehlender Schlüssel) werden gemeldet.
func (r *Compiled) Match(act map[string]any) (bool, error) {
	out, _, err := r.prg.Eval(act)
	if err != nil {
		return false, err
	}
	if types.IsError(out) {
		return false, errors.New("cel: fehler")
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("regel %q liefert kein bool", r.Name)
	}
	return b, nil
}

// RuleSet ist eine geordnete Menge übersetzter Regeln.
type RuleSet []*Compiled

// CompileAll übersetzt alle aktiven Regeln und sortiert nach Priorität (absteigend).
func CompileAll(rules []Rule) (RuleSet, error) {
	var out RuleSet
	var errs []error
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		c, err := Compile(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out, errors.Join(errs...)
}
