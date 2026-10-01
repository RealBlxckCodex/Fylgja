package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WebAppUser ist der Nutzer aus den initData einer Telegram Mini App.
type WebAppUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

var (
	ErrInitData = errors.New("telegram: initData ungültig")
	ErrExpired  = errors.New("telegram: initData abgelaufen")
)

// ValidateInitData prüft die initData einer Mini App nach der Telegram-Vorschrift:
// secret = HMAC_SHA256(key="WebAppData", bot_token); hash = HMAC_SHA256(key=secret, data_check_string).
// Die Signatur beweist nur, dass Telegram die Daten für diesen Bot ausgestellt hat. Wer der Nutzer ist,
// folgt aus dem signierten Feld "user"; ob er zur Fylgja gehört, entscheidet die Kopplung (pairing).
func ValidateInitData(initData, botToken string, maxAge time.Duration, now time.Time) (*WebAppUser, error) {
	if botToken == "" || initData == "" || len(initData) > 8192 {
		return nil, ErrInitData
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return nil, ErrInitData
	}
	hash := vals.Get("hash")
	if hash == "" {
		return nil, ErrInitData
	}
	var pairs []string
	for k, v := range vals {
		if k == "hash" || len(v) != 1 {
			continue
		}
		pairs = append(pairs, k+"="+v[0])
	}
	sort.Strings(pairs)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(pairs, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(hash))) {
		return nil, ErrInitData
	}
	sec, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil {
		return nil, ErrInitData
	}
	at := time.Unix(sec, 0)
	if now.Sub(at) > maxAge || at.Sub(now) > time.Minute {
		return nil, ErrExpired
	}
	var u WebAppUser
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return nil, fmt.Errorf("%w: kein nutzer", ErrInitData)
	}
	return &u, nil
}

// SetMenuButton setzt den Menü-Knopf des Bots so, dass er die Mini App öffnet (Telegram verlangt https).
func (b *Bot) SetMenuButton(ctx context.Context, appURL string) error {
	if !strings.HasPrefix(appURL, "https://") {
		return errors.New("telegram: Mini App braucht eine https-URL (base_url)")
	}
	return b.call(ctx, "setChatMenuButton", map[string]any{"menu_button": map[string]any{"type": "web_app", "text": "Fylgja", "web_app": map[string]string{"url": appURL}}}, nil)
}
