package mcp

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/realblxckcodex/fylgja/internal/policy"
)

func TestClassify(t *testing.T) {
	yes := true
	cases := map[string]policy.Class{
		"list_issues":        policy.Read,
		"get_file":           policy.Read,
		"create_issue":       policy.WriteExternal,
		"send_email":         policy.Communicate,
		"delete_repo":        policy.Destructive,
		"create_payment":     policy.Spend,
		"reset_password":     policy.Credential,
		"do_something_weird": policy.WriteExternal,
	}
	for n, want := range cases {
		if got := Classify(n, nil); got != want {
			t.Errorf("%s: %s want %s", n, got, want)
		}
	}
	if Classify("frobnicate", &sdk.ToolAnnotations{ReadOnlyHint: true}) != policy.Read {
		t.Error("readonly-hint")
	}
	if Classify("update_record", &sdk.ToolAnnotations{DestructiveHint: &yes}) != policy.Destructive {
		t.Error("destructive-hint")
	}
	// Ein "read"-Name mit Versand-Semantik bleibt konservativ.
	if Classify("get_and_send_report", nil) != policy.Communicate {
		t.Error("konservativ")
	}
}
