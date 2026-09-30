package policy

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ApprovalStatus (14.7): pending → approved | denied | expired | cancelled.
type ApprovalStatus string

const (
	Pending   ApprovalStatus = "pending"
	Approved  ApprovalStatus = "approved"
	Denied    ApprovalStatus = "denied"
	Expired   ApprovalStatus = "expired"
	Cancelled ApprovalStatus = "cancelled"
)

// Approval ist der Zustand einer Freigabeanfrage.
type Approval struct {
	ID                string         `json:"id"`
	RunID             string         `json:"run_id"`
	DotID             string         `json:"dot_id"`
	Tool              string         `json:"tool"`
	Class             Class          `json:"class"`
	ArgsRedacted      map[string]any `json:"args_redacted"`
	Preview           map[string]any `json:"preview"`
	Risk              string         `json:"risk"`
	Reason            string         `json:"reason"`
	Status            ApprovalStatus `json:"status"`
	ApproverGroup     string         `json:"approver_group,omitempty"`
	RequiredApprovals int            `json:"required_approvals"`
	StepUp            bool           `json:"step_up"`
	ApprovalsGiven    []string       `json:"approvals_given"` // User-IDs
	ResolvedBy        string         `json:"resolved_by,omitempty"`
	ResolvedVia       string         `json:"resolved_via,omitempty"`
	DenyReason        string         `json:"deny_reason,omitempty"`
	ResolvedAt        time.Time      `json:"resolved_at,omitzero"`
	ExpiresAt         time.Time      `json:"expires_at"`
	CreatedAt         time.Time      `json:"created_at"`
}

var (
	ErrNotPending  = errors.New("approval ist nicht mehr offen")
	ErrSameUser    = errors.New("vier-augen: zweite freigabe muss von anderer person kommen")
	ErrStepUp      = errors.New("step-up-authentifizierung erforderlich")
	ErrNotApprover = errors.New("keine berechtigung zur freigabe")
)

// DefaultExpiry je Klasse (Default 24 h).
func DefaultExpiry(c Class) time.Duration {
	switch c {
	case Spend, Destructive:
		return 4 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// Resolution ist eine Entscheidung eines Menschen.
type Resolution struct {
	UserID     string
	Via        string // web|discord|telegram|api
	Approve    bool
	Reason     string
	StepUpDone bool
	Now        time.Time
}

// Resolve wendet eine Entscheidung an. Liefert true, wenn die Approval final ist.
func (a *Approval) Resolve(r Resolution) (bool, error) {
	if a.Status != Pending {
		return false, ErrNotPending
	}
	if !r.Now.Before(a.ExpiresAt) {
		a.Status, a.ResolvedAt = Expired, r.Now
		return true, ErrNotPending
	}
	if !r.Approve {
		a.Status, a.ResolvedBy, a.ResolvedVia, a.DenyReason, a.ResolvedAt = Denied, r.UserID, r.Via, r.Reason, r.Now
		return true, nil
	}
	if a.StepUp && !r.StepUpDone {
		return false, ErrStepUp
	}
	if slices.Contains(a.ApprovalsGiven, r.UserID) {
		return false, ErrSameUser
	}
	a.ApprovalsGiven = append(a.ApprovalsGiven, r.UserID)
	need := max(a.RequiredApprovals, 1)
	if len(a.ApprovalsGiven) >= need {
		a.Status, a.ResolvedBy, a.ResolvedVia, a.ResolvedAt = Approved, r.UserID, r.Via, r.Now
		return true, nil
	}
	return false, nil
}

// Expire markiert eine abgelaufene Approval.
func (a *Approval) Expire(now time.Time) bool {
	if a.Status == Pending && !now.Before(a.ExpiresAt) {
		a.Status, a.ResolvedAt = Expired, now
		return true
	}
	return false
}

// Cancel bricht eine offene Approval ab (z. B. Run abgebrochen).
func (a *Approval) Cancel(now time.Time) error {
	if a.Status != Pending {
		return ErrNotPending
	}
	a.Status, a.ResolvedAt = Cancelled, now
	return nil
}

// ToolResultText ist das, was die Fylgja nach Ablehnung/Ablauf als Tool-Ergebnis sieht.
func (a *Approval) ToolResultText() string {
	switch a.Status {
	case Denied:
		if a.DenyReason != "" {
			return fmt.Sprintf("abgelehnt: %s. Wiederhole diese Aktion nicht ohne neue Information.", a.DenyReason)
		}
		return "abgelehnt. Wiederhole diese Aktion nicht ohne neue Information."
	case Expired:
		return "abgelaufen: keine Freigabe erteilt. Plane um oder frage nach."
	case Cancelled:
		return "abgebrochen."
	}
	return string(a.Status)
}
