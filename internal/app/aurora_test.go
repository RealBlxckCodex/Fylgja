package app

import (
	"testing"

	"github.com/realblxckcodex/fylgja/internal/platform/config"
)

func TestSeedAurora(t *testing.T) {
	var c config.Config
	seedAurora(&c)
	if len(c.Router.Models) != 0 || len(c.Router.Deployments) != 0 {
		t.Fatal("ohne endpoint darf nichts angelegt werden")
	}
	c.Voice.Aurora = config.Aurora{Endpoint: "http://aurora:11435/", APIKey: "k"}
	seedAurora(&c)
	if len(c.Router.Models) != 2 || len(c.Router.Deployments) != 2 {
		t.Fatalf("%+v", c.Router)
	}
	d := c.Router.Deployments[0]
	if d.Endpoint != "http://aurora:11435/v1" || d.ServedModel != "whisper-turbo" || d.Model != "stt" || d.APIKeyEnv != "FYLGJA_AURORA_API_KEY" || d.Engine != "remote_api" || d.Provider != "local" {
		t.Fatalf("%+v", d)
	}
	if c.Router.Deployments[1].ServedModel != "kokoro-v1" || c.Voice.Aurora.Voice != "af_heart" {
		t.Fatalf("%+v %+v", c.Router.Deployments[1], c.Voice.Aurora)
	}
	for _, m := range c.Router.Models {
		if m.PrivacyClass != "self_hosted" {
			t.Fatalf("privacy: %+v", m)
		}
	}
	// Eigene Definitionen und zweiter Aufruf bleiben unverändert.
	seedAurora(&c)
	if len(c.Router.Models) != 2 || len(c.Router.Deployments) != 2 {
		t.Fatal("seed ist nicht idempotent")
	}
	own := config.Config{}
	own.Voice.Aurora.Endpoint = "http://x:1"
	own.Router.Models = []config.ModelSeed{{Name: "tts", PrivacyClass: "any"}}
	own.Router.Deployments = []config.DeploymentSeed{{Name: "aurora-tts", Model: "tts", Endpoint: "http://eigen/v1"}}
	seedAurora(&own)
	if own.Router.Models[0].PrivacyClass != "any" || own.Router.Deployments[0].Endpoint != "http://eigen/v1" || len(own.Router.Deployments) != 2 {
		t.Fatalf("eigene definition überschrieben: %+v", own.Router)
	}
}
