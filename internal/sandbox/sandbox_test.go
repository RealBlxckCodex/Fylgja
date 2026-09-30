package sandbox

import (
	"testing"

	"github.com/google/uuid"
)

func TestContainerHardening(t *testing.T) {
	d := &Docker{Runtime: "runsc", Network: "fylgja-sandbox", EgressProxy: "http://egressd:3128"}
	c := d.ContainerConfig(Spec{DotID: uuid.New(), Image: "img", Token: "t", MemoryMB: 1024, CPUs: 1, PidsLimit: 256, Egress: "open"})
	h := c["HostConfig"].(map[string]any)
	if h["Runtime"] != "runsc" || h["ReadonlyRootfs"] != true || h["Privileged"] != false || c["User"] != "1000:1000" {
		t.Fatalf("%v", h)
	}
	if caps := h["CapDrop"].([]string); caps[0] != "ALL" {
		t.Fatal("capabilities nicht gedroppt")
	}
	for _, m := range h["Mounts"].([]map[string]any) {
		if m["Type"] != "volume" {
			t.Fatal("host-mount!")
		}
	}
	off := d.ContainerConfig(Spec{DotID: uuid.New(), Egress: "offline"})
	if off["HostConfig"].(map[string]any)["NetworkMode"] != "none" {
		t.Fatal("offline-modus hat netz")
	}
}

func TestDomainBinding(t *testing.T) {
	if !DomainAllowed("login.github.com", []string{"github.com"}) || !DomainAllowed("github.com", []string{"github.com"}) {
		t.Fatal("erlaubte domain abgelehnt")
	}
	if DomainAllowed("github.com.evil.io", []string{"github.com"}) || DomainAllowed("evilgithub.com", []string{"github.com"}) {
		t.Fatal("phishing-domain akzeptiert")
	}
}
