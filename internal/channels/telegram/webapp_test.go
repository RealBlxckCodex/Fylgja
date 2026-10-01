package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signed baut initData wie Telegram.
func signed(token string, at time.Time, user string, extra map[string]string) string {
	vals := map[string]string{"auth_date": strconv.FormatInt(at.Unix(), 10), "user": user, "query_id": "AAH"}
	for k, v := range extra {
		vals[k] = v
	}
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

func TestValidateInitData(t *testing.T) {
	now := time.Now()
	const tok = "123456:ABC-geheim"
	user := `{"id":4711,"first_name":"Sam","username":"sam"}`
	good := signed(tok, now.Add(-5*time.Minute), user, nil)
	u, err := ValidateInitData(good, tok, time.Hour, now)
	if err != nil || u.ID != 4711 || u.Username != "sam" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := ValidateInitData(good, "999:anderer-bot", time.Hour, now); err == nil {
		t.Fatal("falscher bot-token akzeptiert")
	}
	// Nutzer austauschen, Hash unverändert.
	forged := strings.Replace(good, url.QueryEscape(`"id":4711`), url.QueryEscape(`"id":1`), 1)
	if forged == good {
		t.Fatal("test-fälschung wirkungslos")
	}
	if _, err := ValidateInitData(forged, tok, time.Hour, now); err == nil {
		t.Fatal("gefälschter nutzer akzeptiert")
	}
	// Feld ergänzen
	if _, err := ValidateInitData(good+"&start_param=x", tok, time.Hour, now); err == nil {
		t.Fatal("zusätzliches feld akzeptiert")
	}
	old := signed(tok, now.Add(-2*time.Hour), user, nil)
	if _, err := ValidateInitData(old, tok, time.Hour, now); err != ErrExpired {
		t.Fatalf("alt: %v", err)
	}
	future := signed(tok, now.Add(time.Hour), user, nil)
	if _, err := ValidateInitData(future, tok, time.Hour, now); err != ErrExpired {
		t.Fatalf("zukunft: %v", err)
	}
	for _, bad := range []string{"", "hash=abc", "auth_date=1", strings.Repeat("a", 9000)} {
		if _, err := ValidateInitData(bad, tok, time.Hour, now); err == nil {
			t.Errorf("%q akzeptiert", bad[:min(10, len(bad))])
		}
	}
	if _, err := ValidateInitData(good, "", time.Hour, now); err == nil {
		t.Fatal("leerer token")
	}
	if _, err := ValidateInitData(signed(tok, now, `{"first_name":"x"}`, nil), tok, time.Hour, now); err == nil {
		t.Fatal("nutzer ohne id akzeptiert")
	}
}
