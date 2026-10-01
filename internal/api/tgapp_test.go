package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/auth"
	"github.com/realblxckcodex/fylgja/internal/store/testdb"
)

func signInit(token string, at time.Time, user string) string {
	vals := map[string]string{"auth_date": strconv.FormatInt(at.Unix(), 10), "user": user}
	var pairs []string
	for k, v := range vals {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	sec := hmac.New(sha256.New, []byte("WebAppData"))
	sec.Write([]byte(token))
	m := hmac.New(sha256.New, sec.Sum(nil))
	m.Write([]byte(strings.Join(pairs, "\n")))
	q := url.Values{}
	for k, v := range vals {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return q.Encode()
}

func TestTelegramWebAppLogin(t *testing.T) {
	pool, _ := testdb.New(t)
	ctx := context.Background()
	const tok = "123:bot-token"
	svc := &auth.Service{Pool: pool}
	ws := uuid.New()
	pool.Exec(ctx, `INSERT INTO workspaces (id,name) VALUES ($1,'w')`, ws)
	uid, err := svc.CreateUser(ctx, ws, "sam@example.org", "Sam", "sehr-geheimes-passwort", "owner")
	if err != nil {
		t.Fatal(err)
	}
	pool.Exec(ctx, `INSERT INTO channel_identities (user_id, platform, platform_user_id, verified_at) VALUES ($1,'telegram','4711',now())`, uid)
	pool.Exec(ctx, `INSERT INTO channel_identities (user_id, platform, platform_user_id) VALUES ($1,'telegram','999')`, uid) // nicht verifiziert
	s := &Server{Pool: pool, Auth: svc, TelegramToken: tok, limiter: newLimiter(1000, 1000)}

	do := func(initData string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/auth/telegram/webapp", strings.NewReader(`{"init_data":`+strconvQuote(initData)+`}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.telegramWebApp(w, req)
		return w
	}
	now := time.Now()
	ok := do(signInit(tok, now, `{"id":4711,"first_name":"Sam"}`))
	if ok.Code != 200 {
		t.Fatalf("gekoppelter nutzer: %d %s", ok.Code, ok.Body.String())
	}
	c := ok.Result().Cookies()
	if len(c) != 1 || c[0].SameSite != http.SameSiteNoneMode || !c[0].Secure || !c[0].HttpOnly || c[0].MaxAge != int(tgSessionTTL.Seconds()) {
		t.Fatalf("cookie: %+v", c)
	}
	if p, err := svc.Session(ctx, c[0].Value); err != nil || p.UserID != uid {
		t.Fatalf("session: %v %v", p, err)
	}
	for name, body := range map[string]string{
		"unbekannter telegram-nutzer":  signInit(tok, now, `{"id":5}`),
		"nicht verifizierte identität": signInit(tok, now, `{"id":999}`),
		"anderer bot":                  signInit("999:fremd", now, `{"id":4711}`),
		"abgelaufen":                   signInit(tok, now.Add(-3*time.Hour), `{"id":4711}`),
		"müll":                         "foo=bar",
	} {
		if w := do(body); w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	// Ohne konfigurierten Bot ist die Mini App aus.
	s.TelegramToken = ""
	if w := do(signInit(tok, now, `{"id":4711}`)); w.Code != 404 {
		t.Fatalf("mini app ohne token: %d", w.Code)
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }
