package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/policy"
	"github.com/realblxckcodex/fylgja/internal/tools"
)

type fakeComputer struct {
	Computer
	actions []string
	args    []map[string]any
}

func (f *fakeComputer) Desktop(_ context.Context, _ uuid.UUID, action string, args map[string]any) (string, string, error) {
	f.actions = append(f.actions, action)
	f.args = append(f.args, args)
	return action + " ok", "data:image/jpeg;base64,AAAA", nil
}

func (f *fakeComputer) Exec(context.Context, uuid.UUID, string, string, time.Duration) (string, string, int, error) {
	return "", "", 0, nil
}

func TestDesktopTools(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	fc := &fakeComputer{}
	env := &tools.Env{DotID: uuid.NewString(), Services: &Services{Computer: fc}}
	run := func(name, args string) tools.Result {
		tl, ok := r.Get(name)
		if !ok {
			t.Fatalf("tool %s fehlt", name)
		}
		res, err := tl.Handler(context.Background(), tools.Call{Tool: name, Args: json.RawMessage(args), Env: env})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run("desktop.screenshot", `{}`)
	if len(res.Images) != 1 || !res.Untrusted {
		t.Fatalf("screenshot muss Bild liefern und als untrusted gelten: %+v", res)
	}
	run("desktop.click", `{"x":10,"y":20}`)
	run("desktop.click", `{"x":10,"y":20,"double":true}`)
	run("desktop.click", `{"x":10,"y":20,"button":"right"}`)
	run("desktop.click", `{"x":1,"y":2}`) // darf nicht an früheren Aufrufen hängen
	want := []string{"screenshot", "click", "double_click", "right_click", "click"}
	if len(fc.actions) != len(want) {
		t.Fatalf("%v", fc.actions)
	}
	for i := range want {
		if fc.actions[i] != want[i] {
			t.Fatalf("aktion %d: %s statt %s", i, fc.actions[i], want[i])
		}
	}
	for name, class := range map[string]policy.Class{"desktop.screenshot": policy.Read, "desktop.click": policy.Compute, "desktop.type": policy.Compute, "desktop.key": policy.Compute} {
		tl, _ := r.Get(name)
		if tl.Class != class {
			t.Errorf("%s: klasse %s", name, tl.Class)
		}
		if !tl.Base {
			t.Errorf("%s sollte im Basis-Set sein", name)
		}
	}
	// Ohne Computer: sauberer Fehler statt Panic.
	tl, _ := r.Get("desktop.click")
	res, _ = tl.Handler(context.Background(), tools.Call{Args: json.RawMessage(`{"x":1,"y":1}`), Env: &tools.Env{DotID: uuid.NewString(), Services: &Services{}}})
	if !res.IsError {
		t.Fatal("fehler ohne computer erwartet")
	}
}
