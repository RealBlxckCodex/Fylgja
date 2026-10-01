package api

import (
	"github.com/realblxckcodex/fylgja/internal/audit"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

// microsoftStatus zeigt, ob und wie eine Fylgja mit Microsoft verbunden ist.
func (s *Server) microsoftStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	out := map[string]any{"available": s.Microsoft != nil && s.Microsoft.Cfg.Enabled(), "connected": false}
	var label, mode string
	err = s.Pool.QueryRow(r.Context(), `SELECT cn.account_label, cn.access_mode FROM connections cn JOIN connectors c ON c.id=cn.connector_id
		WHERE c.kind='microsoft' AND c.workspace_id=$1 AND cn.dot_id=$2 AND cn.status='active'`, principal(r).WorkspaceID, id).Scan(&label, &mode)
	if err == nil {
		out["connected"], out["account"], out["access_mode"] = true, label, mode
	}
	writeJSON(w, 200, out)
}

// microsoftConnect liefert die Microsoft-Einwilligungs-URL (Step-up nötig: gibt Zugriff auf ein Postfach).
func (s *Server) microsoftConnect(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	if s.Microsoft == nil {
		problem(w, 501, "microsoft-connector nicht konfiguriert")
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if in.Mode == "" {
		in.Mode = "read"
	}
	u, err := s.Microsoft.AuthURL(principal(r).WorkspaceID, id, in.Mode)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "microsoft.connect_start", id.String(), map[string]any{"mode": in.Mode})
	writeJSON(w, 200, map[string]string{"url": u})
}

// microsoftCallback ist das Redirect-Ziel von Microsoft. Es gibt keine Sitzung, die Prüfung läuft
// über den signierten State (Workspace, Fylgja, Modus, Ablauf).
func (s *Server) microsoftCallback(w http.ResponseWriter, r *http.Request) {
	if s.Microsoft == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	back := func(dot uuid.UUID, result, msg string) {
		v := url.Values{"tab": {"channels"}, "microsoft": {result}}
		if msg != "" {
			v.Set("msg", msg)
		}
		target := "/"
		if dot != uuid.Nil {
			target = "/dots/" + dot.String()
		}
		http.Redirect(w, r, target+"?"+v.Encode(), http.StatusSeeOther)
	}
	if e := q.Get("error"); e != "" {
		back(uuid.Nil, "error", "Microsoft: "+e)
		return
	}
	dot, err := s.Microsoft.HandleCallback(r.Context(), q.Get("code"), q.Get("state"))
	if err != nil {
		s.Log.Warn("microsoft-callback", "err", err)
		back(dot, "error", err.Error())
		return
	}
	if s.Audit != nil {
		var ws uuid.UUID
		if s.Pool.QueryRow(r.Context(), `SELECT workspace_id FROM dots WHERE id=$1`, dot).Scan(&ws) == nil {
			_ = s.Audit.Log(r.Context(), audit.Entry{WorkspaceID: ws, Actor: "system:microsoft", Action: "microsoft.connected", Target: dot.String()})
		}
	}
	back(dot, "ok", "")
}

func (s *Server) microsoftDisconnect(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil || s.dotInWorkspace(r, id) != nil {
		problem(w, 404, "fylgja nicht gefunden")
		return
	}
	if s.Microsoft == nil {
		problem(w, 501, "microsoft-connector nicht konfiguriert")
		return
	}
	if err := s.Microsoft.Disconnect(r.Context(), principal(r).WorkspaceID, id); err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.audit(r, "microsoft.disconnect", id.String(), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}
