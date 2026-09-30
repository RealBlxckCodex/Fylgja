package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.yaml")
	os.WriteFile(p, []byte(`
database_url: postgres://x
router:
  models:
    - {name: worker-default, privacy_class: self_hosted, quality_rank: 1}
  deployments:
    - {name: a, model: worker-default, provider: local, engine: ollama, endpoint: http://x}
  tiers: {worker: worker-default}
`), 0o600)
	t.Setenv("FYLGJA_MASTER_KEY", "k")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Router.Tiers["worker"] != "worker-default" {
		t.Fatal("tiers not loaded")
	}
}

func TestLoadRejectsUnknownFieldsAndBadRefs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.yaml")
	os.WriteFile(p, []byte("database_url: x\nbogus: 1\n"), 0o600)
	t.Setenv("FYLGJA_MASTER_KEY", "k")
	if _, err := Load(p); err == nil {
		t.Fatal("unknown field accepted")
	}
	os.WriteFile(p, []byte("database_url: x\nrouter: {tiers: {planner: nope}}\n"), 0o600)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "unbekanntes Modell") {
		t.Fatalf("want unknown model error, got %v", err)
	}
}

func TestMissingMasterKey(t *testing.T) {
	t.Setenv("FYLGJA_MASTER_KEY", "")
	t.Setenv("FYLGJA_DATABASE_URL", "postgres://x")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error")
	}
}
