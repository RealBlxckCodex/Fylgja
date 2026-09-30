package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/realblxckcodex/fylgja/internal/coord"
	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

func chiParam(r *http.Request, k string) string { return chi.URLParam(r, k) }

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principal(r)
	rows, err := s.Pool.Query(ctx, `SELECT t.id, t.name, t.lead_dot_id, d.name, t.charter, t.status FROM teams t JOIN dots d ON d.id=t.lead_dot_id WHERE t.workspace_id=$1 ORDER BY t.name`, p.WorkspaceID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	type member struct {
		DotID uuid.UUID `json:"dot_id"`
		Name  string    `json:"name"`
		Role  string    `json:"role"`
		Busy  int       `json:"open_nodes"`
	}
	type team struct {
		ID       uuid.UUID `json:"id"`
		Name     string    `json:"name"`
		LeadID   uuid.UUID `json:"lead_dot_id"`
		LeadName string    `json:"lead_name"`
		Charter  string    `json:"charter"`
		Status   string    `json:"status"`
		Members  []member  `json:"members"`
	}
	var out []team
	for rows.Next() {
		var t team
		_ = rows.Scan(&t.ID, &t.Name, &t.LeadID, &t.LeadName, &t.Charter, &t.Status)
		out = append(out, t)
	}
	rows.Close()
	for i := range out {
		mr, err := s.Pool.Query(ctx, `SELECT m.dot_id, d.name, m.role, (SELECT count(*) FROM work_nodes n WHERE n.owner_dot_id=m.dot_id AND n.status IN ('running','ready','blocked'))
			FROM team_members m JOIN dots d ON d.id=m.dot_id WHERE m.team_id=$1`, out[i].ID)
		if err != nil {
			continue
		}
		for mr.Next() {
			var m member
			_ = mr.Scan(&m.DotID, &m.Name, &m.Role, &m.Busy)
			out[i].Members = append(out[i].Members, m)
		}
		mr.Close()
	}
	if out == nil {
		out = []team{}
	}
	writeJSON(w, 200, out)
}

// createTeam ändert die Team-Zusammensetzung – nur Menschen (17.10 "keine Selbst-Beförderung").
func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Lead    string   `json:"lead_dot_id"`
		Members []string `json:"members"`
		Charter string   `json:"charter"`
	}
	if err := decode(r, &in); err != nil {
		problem(w, 400, err.Error())
		return
	}
	lead, err := uuid.Parse(in.Lead)
	if err != nil || s.dotInWorkspace(r, lead) != nil {
		problem(w, 400, "lead ungültig")
		return
	}
	id := ids.New()
	ctx := r.Context()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO teams (id, workspace_id, name, lead_dot_id, charter) VALUES ($1,$2,$3,$4,$5)`, id, principal(r).WorkspaceID, in.Name, lead, in.Charter); err != nil {
		problem(w, 400, err.Error())
		return
	}
	_, _ = s.Pool.Exec(ctx, `INSERT INTO team_members (team_id, dot_id, role) VALUES ($1,$2,'lead')`, id, lead)
	for _, m := range in.Members {
		if mid, err := uuid.Parse(m); err == nil && s.dotInWorkspace(r, mid) == nil && mid != lead {
			_, _ = s.Pool.Exec(ctx, `INSERT INTO team_members (team_id, dot_id, role) VALUES ($1,$2,'member') ON CONFLICT DO NOTHING`, id, mid)
		}
	}
	s.audit(r, "team.create", id.String(), map[string]any{"name": in.Name, "members": in.Members})
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) listGraphs(w http.ResponseWriter, r *http.Request) {
	list, err := s.Coord.Store.ListGraphs(r.Context(), principal(r).WorkspaceID, 100)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []map[string]any{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) graphOf(r *http.Request) (*coord.Graph, error) {
	g, err := s.Coord.Store.LoadGraph(r.Context(), chiParam(r, "id"))
	if err != nil {
		return nil, err
	}
	if s.dotInWorkspace(r, uuid.MustParse(g.LeadDotID)) != nil {
		return nil, errForbidden
	}
	return g, nil
}

func (s *Server) getGraph(w http.ResponseWriter, r *http.Request) {
	g, err := s.graphOf(r)
	if err != nil {
		problem(w, 404, "graph nicht gefunden")
		return
	}
	// Kosten je Knoten (über Run-Nutzung).
	costs := map[string]int64{}
	for _, n := range g.Nodes {
		if n.RunID != "" {
			if id, err := uuid.Parse(n.RunID); err == nil {
				_, c, _ := s.Runtime.Store.RunUsage(r.Context(), id)
				costs[n.ID] = c
			}
		}
	}
	names := map[string]string{}
	rows, _ := s.Pool.Query(r.Context(), `SELECT id, name FROM dots WHERE workspace_id=$1`, principal(r).WorkspaceID)
	if rows != nil {
		for rows.Next() {
			var id uuid.UUID
			var n string
			_ = rows.Scan(&id, &n)
			names[id.String()] = n
		}
		rows.Close()
	}
	var history []map[string]any
	hr, err := s.Pool.Query(r.Context(), `SELECT id, history FROM work_nodes WHERE graph_id=$1`, g.ID)
	if err == nil {
		for hr.Next() {
			var id uuid.UUID
			var h []byte
			_ = hr.Scan(&id, &h)
			history = append(history, map[string]any{"node": id, "history": json.RawMessage(h)})
		}
		hr.Close()
	}
	writeJSON(w, 200, map[string]any{"graph": g, "estimate": g.EstimatePlan(2, []string{"mail.send", "message.send", "shell.run"}),
		"critical_path": g.CriticalPath(), "costs": costs, "dot_names": names, "history": history, "remaining": coord.Remaining(g)})
}

func (s *Server) planGraph(w http.ResponseWriter, r *http.Request) {
	var g coord.Graph
	if err := decode(r, &g); err != nil {
		problem(w, 400, err.Error())
		return
	}
	lead, err := uuid.Parse(g.LeadDotID)
	if err != nil || s.dotInWorkspace(r, lead) != nil {
		problem(w, 400, "lead_dot_id ungültig")
		return
	}
	if err := s.Coord.Plan(r.Context(), &g, nil); err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.audit(r, "graph.plan", g.ID, map[string]any{"nodes": len(g.Nodes)})
	writeJSON(w, 201, g)
}

func (s *Server) graphAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, err := s.graphOf(r)
		if err != nil {
			problem(w, 404, "graph nicht gefunden")
			return
		}
		ctx := r.Context()
		node := chiParam(r, "node")
		switch action {
		case "start":
			err = s.Coord.Start(ctx, g.ID)
		case "cancel":
			err = s.Coord.Cancel(ctx, g.ID, node)
		case "retry":
			err = s.Coord.Reassign(ctx, g.ID, node, g.Node(node).OwnerKind, g.Node(node).OwnerDotID, "")
		case "reassign":
			var in struct {
				OwnerKind string `json:"owner_kind"`
				DotID     string `json:"dot_id"`
				Tier      string `json:"tier"`
			}
			if err := decode(r, &in); err != nil {
				problem(w, 400, err.Error())
				return
			}
			err = s.Coord.Reassign(ctx, g.ID, node, coord.OwnerKind(in.OwnerKind), in.DotID, in.Tier)
		case "steer":
			var in struct {
				Text string `json:"text"`
			}
			if err := decode(r, &in); err != nil {
				problem(w, 400, err.Error())
				return
			}
			n := g.Node(node)
			if n == nil || n.RunID == "" || !s.Runtime.Steer(uuid.MustParse(n.RunID), in.Text) {
				problem(w, 409, "knoten läuft nicht")
				return
			}
		case "complete": // Mensch als Knoten (Entscheidung/Input)
			var in struct {
				Result json.RawMessage `json:"result"`
			}
			if err := decode(r, &in); err != nil {
				problem(w, 400, err.Error())
				return
			}
			n := g.Node(node)
			if n == nil || n.OwnerKind != coord.OwnerHuman {
				problem(w, 400, "nur menschliche knoten können hier abgeschlossen werden")
				return
			}
			if n.Status == coord.Ready {
				_ = s.Coord.Store.SetStatus(ctx, n, coord.Running, nil)
			}
			err = s.Coord.Complete(ctx, g.ID, node, in.Result, "")
		}
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		s.audit(r, "graph."+action, g.ID, map[string]any{"node": node})
		writeJSON(w, 200, map[string]any{"ok": true, "at": time.Now()})
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), `SELECT t.id, t.dot_id, d.name, t.title, t.goal, t.status, t.priority, t.due_at, t.created_at, t.updated_at
		FROM tasks t JOIN dots d ON d.id=t.dot_id WHERE d.workspace_id=$1 ORDER BY t.updated_at DESC LIMIT 200`, principal(r).WorkspaceID)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, dot uuid.UUID
		var name, title, goal, status string
		var prio int
		var due *time.Time
		var created, updated time.Time
		_ = rows.Scan(&id, &dot, &name, &title, &goal, &status, &prio, &due, &created, &updated)
		out = append(out, map[string]any{"id": id, "dot_id": dot, "dot_name": name, "title": title, "goal": goal, "status": status, "priority": prio, "due_at": due, "created_at": created, "updated_at": updated})
	}
	writeJSON(w, 200, out)
}
