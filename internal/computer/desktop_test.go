package computer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type rec struct {
	calls [][]string
	size  string
}

func (r *rec) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	switch {
	case name == "xdotool" && args[0] == "getdisplaygeometry":
		if r.size == "" {
			return []byte("1440 900\n"), nil
		}
		return []byte(r.size), nil
	case name == "import":
		return []byte("\xff\xd8\xff fake jpeg"), nil
	}
	return nil, nil
}

func (r *rec) last() string { return strings.Join(r.calls[len(r.calls)-1], " ") }

func ip(i int) *int { return &i }

func TestDesktopActions(t *testing.T) {
	r := &rec{}
	s := &Server{Run: r.run}
	ctx := context.Background()
	if _, err := s.desktopAction(ctx, "click", desktopReq{X: ip(100), Y: ip(200)}); err != nil || r.last() != "xdotool mousemove --sync 100 200 click 1" {
		t.Fatalf("click: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "double_click", desktopReq{X: ip(5), Y: ip(6)}); err != nil || !strings.Contains(r.last(), "--repeat 2") {
		t.Fatalf("double: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "drag", desktopReq{X: ip(1), Y: ip(2), ToX: ip(30), ToY: ip(40)}); err != nil || r.last() != "xdotool mousemove --sync 1 2 mousedown 1 mousemove --sync 30 40 mouseup 1" {
		t.Fatalf("drag: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "type", desktopReq{Text: "--version; rm -rf /"}); err != nil || r.last() != "xdotool type --delay 12 --clearmodifiers -- --version; rm -rf /" {
		t.Fatalf("type: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "key", desktopReq{Keys: "ctrl+l Return"}); err != nil || r.last() != "xdotool key --clearmodifiers -- ctrl+l Return" {
		t.Fatalf("key: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "scroll", desktopReq{DY: -3}); err != nil || r.last() != "xdotool click --repeat 3 --delay 30 4" {
		t.Fatalf("scroll: %v %q", err, r.last())
	}
	if _, err := s.desktopAction(ctx, "scroll", desktopReq{DY: 9999}); err != nil || !strings.Contains(r.last(), "--repeat 30") {
		t.Fatalf("scroll cap: %v %q", err, r.last())
	}
}

func TestDesktopRejectsBadInput(t *testing.T) {
	r := &rec{}
	s := &Server{Run: r.run}
	ctx := context.Background()
	bad := []struct {
		a string
		q desktopReq
	}{
		{"click", desktopReq{X: ip(-1), Y: ip(5)}},
		{"click", desktopReq{X: ip(1440), Y: ip(5)}},
		{"click", desktopReq{X: ip(5)}},
		{"drag", desktopReq{X: ip(1), Y: ip(1), ToX: ip(9999), ToY: ip(1)}},
		{"type", desktopReq{}},
		{"type", desktopReq{Text: strings.Repeat("a", maxTypeChars+1)}},
		{"key", desktopReq{}},
		{"key", desktopReq{Keys: "ctrl+l; reboot"}},
		{"key", desktopReq{Keys: "a b c d e f g h i"}},
		{"key", desktopReq{Keys: "--help"}},
		{"explode", desktopReq{}},
	}
	for _, b := range bad {
		before := len(r.calls)
		if _, err := s.desktopAction(ctx, b.a, b.q); err == nil {
			t.Errorf("%s %+v wurde akzeptiert", b.a, b.q)
		}
		for _, c := range r.calls[before:] {
			if c[0] == "xdotool" && c[1] != "getdisplaygeometry" {
				t.Errorf("%s %+v hat trotz Fehler xdotool aufgerufen: %v", b.a, b.q, c)
			}
		}
	}
}

func TestScreenshot(t *testing.T) {
	r := &rec{size: "1024 768\n"}
	s := &Server{Run: r.run}
	res, err := s.desktopAction(context.Background(), "screenshot", desktopReq{})
	if err != nil || !strings.HasPrefix(res.Image, "data:image/jpeg;base64,") || res.Width != 1024 || res.Height != 768 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDesktopHTTPDecodesAllFields(t *testing.T) {
	r := &rec{}
	s := &Server{Token: strings.Repeat("t", 32), Run: r.run}
	h := s.Handler()
	do := func(action, body string) int {
		req := httptest.NewRequest("POST", "/v1/desktop/"+action, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := do("click", `{"x":11,"y":22}`); c != 200 || r.last() != "xdotool mousemove --sync 11 22 click 1" {
		t.Fatalf("click: %d %q", c, r.last())
	}
	if c := do("drag", `{"x":1,"y":2,"to_x":3,"to_y":4}`); c != 200 || !strings.Contains(r.last(), "mousemove --sync 3 4") {
		t.Fatalf("drag: %d %q", c, r.last())
	}
	if c := do("scroll", `{"dy":-2}`); c != 200 || !strings.Contains(r.last(), "--repeat 2") {
		t.Fatalf("scroll: %d %q", c, r.last())
	}
	if c := do("click", `{"x":11}`); c != 400 {
		t.Fatalf("fehlendes y: %d", c)
	}
	req := httptest.NewRequest("POST", "/v1/desktop/click", strings.NewReader(`{"x":1,"y":1}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("ohne token: %d", w.Code)
	}
}
