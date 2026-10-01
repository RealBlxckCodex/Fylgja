package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/channels/telegram"
)

const tgSessionTTL = 8 * time.Hour

// telegramWebApp meldet jemanden aus einer Telegram Mini App an. Voraussetzungen: gültige, frische initData
// dieses Bots und eine bereits gekoppelte, verifizierte Telegram-Identität. Fremde bekommen keinen Zugang
// und keine Auskunft darüber, ob ein Konto existiert.
func (s *Server) telegramWebApp(w http.ResponseWriter, r *http.Request) {
	if s.TelegramToken == "" {
		problem(w, 404, "mini app nicht aktiv")
		return
	}
	if !s.limiter.allow("tgapp:" + clientIP(r)) {
		problem(w, 429, "zu viele anmeldeversuche")
		return
	}
	var in struct {
		InitData string `json:"init_data"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	u, err := telegram.ValidateInitData(in.InitData, s.TelegramToken, time.Hour, time.Now())
	if err != nil {
		problem(w, 401, "nicht autorisiert")
		return
	}
	var uid uuid.UUID
	err = s.Pool.QueryRow(r.Context(), `SELECT user_id FROM channel_identities WHERE platform='telegram' AND platform_user_id=$1 AND verified_at IS NOT NULL`,
		strconv.FormatInt(u.ID, 10)).Scan(&uid)
	if err != nil {
		problem(w, 401, "nicht autorisiert")
		return
	}
	tok, p, err := s.Auth.NewSession(r.Context(), uid, false)
	if err != nil {
		problem(w, 401, "nicht autorisiert")
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Log(r.Context(), audit.Entry{WorkspaceID: p.WorkspaceID, Actor: "user:" + p.UserID.String(), Action: "auth.login",
			Detail: map[string]any{"method": "telegram_webapp", "telegram_id": u.ID, "ip": clientIP(r)}})
	}
	// SameSite=None, weil web.telegram.org die App in einem iframe einbettet; Telegram verlangt https.
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, MaxAge: int(tgSessionTTL.Seconds())})
	writeJSON(w, 200, map[string]any{"principal": p})
}
