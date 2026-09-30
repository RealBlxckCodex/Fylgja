package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/realblxckcodex/fylgja/internal/platform/ids"
)

// Store persistiert Graphen. Knotenübergänge laufen nur über SetStatus (compare-and-set).
type Store struct{ Pool *pgxpool.Pool }

// CreateGraph speichert einen neuen Graphen samt Knoten und Kanten.
func (s *Store) CreateGraph(ctx context.Context, g *Graph) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if g.ID == "" {
		g.ID = ids.New().String()
	}
	if g.Status == "" {
		g.Status = "planned"
	}
	if g.PlanVersion == 0 {
		g.PlanVersion = 1
	}
	budget, _ := json.Marshal(g.Budget)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var team any
		if g.TeamID != "" {
			team = g.TeamID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_graphs (id, team_id, lead_dot_id, title, status, plan_version, budget) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			g.ID, team, g.LeadDotID, g.Title, g.Status, g.PlanVersion, budget); err != nil {
			return err
		}
		idmap := map[string]string{}
		for _, n := range g.Nodes {
			old := n.ID
			if _, err := uuid.Parse(n.ID); err != nil {
				n.ID = ids.New().String()
			}
			idmap[old] = n.ID
			n.GraphID = g.ID
			if n.Status == "" {
				n.Status = Pending
			}
			if err := insertNode(ctx, tx, n); err != nil {
				return err
			}
		}
		for i, e := range g.Edges {
			e.From, e.To = idmap[e.From], idmap[e.To]
			g.Edges[i] = e
			if _, err := tx.Exec(ctx, `INSERT INTO work_edges (graph_id, from_node, to_node, kind) VALUES ($1,$2,$3,$4)`, g.ID, e.From, e.To, e.Kind); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertNode(ctx context.Context, tx pgx.Tx, n *Node) error {
	contract, _ := json.Marshal(n.Contract)
	var owner, worker any
	if n.OwnerDotID != "" {
		owner = n.OwnerDotID
	}
	if n.WorkerID != "" {
		worker = n.WorkerID
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_nodes (id, graph_id, title, goal, owner_kind, owner_dot_id, worker_id, status, contract, rationale)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, n.ID, n.GraphID, n.Title, n.Goal, n.OwnerKind, owner, worker, n.Status, contract, n.Rationale)
	return err
}

// AddNode fügt einem bestehenden Graphen einen Knoten hinzu (Re-Plan).
func (s *Store) AddNode(ctx context.Context, n *Node, deps []string) error {
	if n.ID == "" {
		n.ID = ids.New().String()
	}
	n.Status = Pending
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := insertNode(ctx, tx, n); err != nil {
			return err
		}
		for _, d := range deps {
			if _, err := tx.Exec(ctx, `INSERT INTO work_edges (graph_id, from_node, to_node, kind) VALUES ($1,$2,$3,'depends_on')`, n.GraphID, d, n.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE work_graphs SET plan_version=plan_version+1 WHERE id=$1`, n.GraphID)
		return err
	})
}

// LoadGraph lädt einen Graphen vollständig.
func (s *Store) LoadGraph(ctx context.Context, id string) (*Graph, error) {
	g := &Graph{ID: id}
	var team *uuid.UUID
	var lead uuid.UUID
	var budget []byte
	err := s.Pool.QueryRow(ctx, `SELECT team_id, lead_dot_id, title, status, plan_version, budget FROM work_graphs WHERE id=$1`, id).
		Scan(&team, &lead, &g.Title, &g.Status, &g.PlanVersion, &budget)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("coord: graph %s nicht gefunden", id)
		}
		return nil, err
	}
	if team != nil {
		g.TeamID = team.String()
	}
	g.LeadDotID = lead.String()
	_ = json.Unmarshal(budget, &g.Budget)
	rows, err := s.Pool.Query(ctx, `SELECT id, title, goal, owner_kind, coalesce(owner_dot_id::text,''), coalesce(worker_id::text,''), status, contract,
		result, coalesce(result_ref,''), attempt, notes, reason, rationale, coalesce(run_id::text,''), updated_at FROM work_nodes WHERE graph_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n := &Node{GraphID: id}
		var nid uuid.UUID
		var contract, result []byte
		if err := rows.Scan(&nid, &n.Title, &n.Goal, &n.OwnerKind, &n.OwnerDotID, &n.WorkerID, &n.Status, &contract, &result, &n.ResultRef, &n.Attempt,
			&n.Notes, &n.Reason, &n.Rationale, &n.RunID, &n.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		n.ID = nid.String()
		_ = json.Unmarshal(contract, &n.Contract)
		if len(result) > 0 {
			n.Result = result
		}
		g.Nodes = append(g.Nodes, n)
	}
	rows.Close()
	erows, err := s.Pool.Query(ctx, `SELECT from_node, to_node, kind FROM work_edges WHERE graph_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var f, t uuid.UUID
		var e Edge
		if err := erows.Scan(&f, &t, &e.Kind); err != nil {
			return nil, err
		}
		e.From, e.To = f.String(), t.String()
		g.Edges = append(g.Edges, e)
	}
	return g, erows.Err()
}

// SetStatus führt einen geprüften Übergang durch (compare-and-set gegen Nebenläufigkeit).
func (s *Store) SetStatus(ctx context.Context, n *Node, to NodeStatus, mut func(n *Node)) error {
	if err := CanTransition(n.Status, to); err != nil {
		return err
	}
	from := n.Status
	if mut != nil {
		mut(n)
	}
	var result any
	if len(n.Result) > 0 {
		result = []byte(n.Result)
	}
	var runID, worker, owner any
	if n.RunID != "" {
		runID = n.RunID
	}
	if n.WorkerID != "" {
		worker = n.WorkerID
	}
	if n.OwnerDotID != "" {
		owner = n.OwnerDotID
	}
	contract, _ := json.Marshal(n.Contract)
	hist, _ := json.Marshal(map[string]any{"from": from, "to": to, "at": time.Now(), "reason": n.Reason, "attempt": n.Attempt})
	tag, err := s.Pool.Exec(ctx, `UPDATE work_nodes SET status=$3, result=$4, attempt=$5, notes=$6, reason=$7, run_id=$8, worker_id=$9,
			owner_kind=$10, owner_dot_id=$11, contract=$12, history = history || jsonb_build_array($13::jsonb), updated_at=now()
		WHERE id=$1 AND status=$2`, n.ID, from, to, result, n.Attempt, n.Notes, n.Reason, runID, worker, n.OwnerKind, owner, contract, hist)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: knoten %s nicht mehr im zustand %s", ErrTransition, n.ID, from)
	}
	n.Status = to
	n.UpdatedAt = time.Now()
	return nil
}

// SetReason aktualisiert nur den Wartegrund (ohne Übergang).
func (s *Store) SetReason(ctx context.Context, n *Node, reason string) error {
	if n.Reason == reason {
		return nil
	}
	n.Reason = reason
	_, err := s.Pool.Exec(ctx, `UPDATE work_nodes SET reason=$2 WHERE id=$1`, n.ID, reason)
	return err
}

// SetGraphStatus setzt den Graph-Status.
func (s *Store) SetGraphStatus(ctx context.Context, id, status string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE work_graphs SET status=$2 WHERE id=$1`, id, status)
	return err
}

// ListGraphs liefert Graphen einer Lead-Fylgja bzw. aller Fylgjur eines Workspaces.
func (s *Store) ListGraphs(ctx context.Context, ws uuid.UUID, limit int) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT g.id, g.title, g.status, g.lead_dot_id, d.name, g.created_at,
			(SELECT count(*) FROM work_nodes n WHERE n.graph_id=g.id),
			(SELECT count(*) FROM work_nodes n WHERE n.graph_id=g.id AND n.status='done')
		FROM work_graphs g JOIN dots d ON d.id=g.lead_dot_id WHERE d.workspace_id=$1 ORDER BY g.created_at DESC LIMIT $2`, ws, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, lead uuid.UUID
		var title, status, leadName string
		var created time.Time
		var total, done int
		if err := rows.Scan(&id, &title, &status, &lead, &leadName, &created, &total, &done); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "title": title, "status": status, "lead_dot_id": lead, "lead_name": leadName, "created_at": created, "nodes": total, "done": done})
	}
	return out, rows.Err()
}

// RunningWorkers zählt laufende Worker-Knoten (Limits, 17.8).
func (s *Store) RunningWorkers(ctx context.Context, leadDot string, ws uuid.UUID) (perLead, perWorkspace int, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE g.lead_dot_id=$1),
			count(*)
		FROM work_nodes n JOIN work_graphs g ON g.id=n.graph_id JOIN dots d ON d.id=g.lead_dot_id
		WHERE n.owner_kind='worker' AND n.status IN ('running','needs_review','blocked') AND d.workspace_id=$2`, leadDot, ws).Scan(&perLead, &perWorkspace)
	return
}
