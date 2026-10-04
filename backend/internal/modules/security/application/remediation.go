package application

import (
	"context"
	"fmt"
	"time"

	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/jackc/pgx/v5"
)

// Security owns the Advisory to Change relationship. It does not change the Change.
var Triples = []relationships.Triple{{SourceType: "advisory", Type: "REMEDIATED_BY", TargetType: "change", Owner: "security"}}

const MaxRemediationTasks = 50
const MaxRemediationChanges = 25

// RemediationTask is a live Task projection. Assignee IDs and names are omitted
// unless the caller has tasks.view; status and due date are always shown.
type RemediationTask struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	DueAt          *time.Time `json:"dueAt"`
	AssignedUserID *string    `json:"assignedUserId,omitempty"`
	AssignedTeamID *string    `json:"assignedTeamId,omitempty"`
	AssigneeName   *string    `json:"assigneeName,omitempty"`
	AssigneeHidden bool       `json:"assigneeHidden"`
}

type ChangeLink struct {
	ID        string `json:"id"`
	ChangeID  string `json:"changeId,omitempty"`
	Reference string `json:"reference,omitempty"`
	Status    string `json:"status,omitempty"`
	Hidden    bool   `json:"hidden"`
}

type ChangeReader interface {
	Lookup(context.Context, []string, changespublic.ReadScope) (map[string]changespublic.ChangeInfo, error)
}
type TaskCreator interface {
	CreateInTx(context.Context, pgx.Tx, taskspublic.Caller, taskspublic.CreateInput) (string, error)
	ByContext(context.Context, string, string, int) ([]taskspublic.Task, error)
	ByContextInTx(context.Context, pgx.Tx, string, string, int) ([]taskspublic.Task, error)
	SummaryByContexts(context.Context, string, []string) (taskspublic.ContextSummary, error)
	SummaryByType(context.Context, string) (taskspublic.ContextSummary, error)
}
type AssigneeNames interface {
	UserNames(context.Context, []string) (map[string]string, error)
	TeamNames(context.Context, []string) (map[string]string, error)
}

// WithRemediation connects shared Tasks, Changes and Relationships contracts.
func (s *Service) WithRemediation(tasks TaskCreator, changes ChangeReader, graph *relationships.Graph, names AssigneeNames, pool relationships.Querier) *Service {
	s.tasks, s.changes, s.graph, s.names, s.relationshipsDB = tasks, changes, graph, names, pool
	return s
}

func contextType(kind string) (string, error) {
	switch kind {
	case "advisory":
		return "security_advisory", nil
	case "finding":
		return "security_finding", nil
	}
	return "", invalid("kind must be advisory or finding")
}

func (s *Service) checkObject(ctx context.Context, kind, id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", ErrNotFound
	}
	switch kind {
	case "advisory":
		a, e := s.store.GetAdvisory(ctx, id)
		return a.Reference, e
	case "finding":
		f, e := s.store.GetFinding(ctx, id)
		return f.Reference, e
	}
	return "", ErrNotFound
}

// CreateRemediationTask creates an ordinary Task with a fixed reference-based title.
// Task completion never changes a Finding: only fresh inventory evidence can remediate it.
func (s *Service) CreateRemediationTask(ctx context.Context, c Caller, p Principal, kind, id string, expected *int, user, team *string, due *time.Time) (string, error) {
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return "", ErrForbidden
	}
	if err := c.validate(); err != nil {
		return "", err
	}
	version, err := requireVersion(expected)
	if err != nil {
		return "", err
	}
	typ, err := contextType(kind)
	if err != nil {
		return "", err
	}
	if !uuidPattern.MatchString(id) {
		return "", ErrNotFound
	}
	if due == nil || due.Before(s.now()) || due.After(s.now().AddDate(1, 0, 0)) {
		return "", invalid("dueAt must be within one year")
	}
	if (user == nil || *user == "") && (team == nil || *team == "") {
		return "", invalid("assignee is required")
	}
	var result string
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		var ref string
		var advisory *Advisory
		var finding *Finding
		if kind == "advisory" {
			a, e := s.lockAdvisory(ctx, tx, id, version, "create_remediation_task")
			if e != nil {
				return e
			}
			ref = a.Reference
			advisory = &a
		} else {
			f, e := s.lockFinding(ctx, tx, id, version, "create_remediation_task", FindingStatuses...)
			if e != nil {
				return e
			}
			ref = f.Reference
			finding = &f
		}
		existing, e := s.tasks.ByContextInTx(ctx, tx, typ, id, MaxRemediationTasks+1)
		if e != nil {
			return e
		}
		if len(existing) >= MaxRemediationTasks {
			return invalid("remediation task limit reached")
		}
		title := "Remediate security advisory " + ref
		if kind == "finding" {
			title = "Remediate vulnerability finding " + ref
		}
		result, e = s.tasks.CreateInTx(ctx, tx, taskspublic.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}, taskspublic.CreateInput{Title: title, ContextType: typ, ContextID: id, AssignedUserID: user, AssignedTeamID: team, DueAt: due})
		if e != nil {
			return e
		}
		if advisory != nil {
			if _, e = s.store.UpdateAdvisoryTx(ctx, tx, *advisory); e != nil {
				return e
			}
		} else {
			if _, e = s.store.UpdateFindingTx(ctx, tx, *finding); e != nil {
				return e
			}
		}
		return recordAudit(ctx, tx, c, "security.remediation_task.created", kind, id, nil, map[string]any{"taskId": result}, nil)
	})
	return result, err
}

// RemediationTasks returns live Task states; no Task event projection is used.
func (s *Service) RemediationTasks(ctx context.Context, p Principal, kind, id string) ([]RemediationTask, error) {
	if !p.reads() {
		return nil, ErrForbidden
	}
	typ, err := contextType(kind)
	if err != nil {
		return nil, err
	}
	if _, err = s.checkObject(ctx, kind, id); err != nil {
		return nil, err
	}
	tasks, err := s.tasks.ByContext(ctx, typ, id, MaxRemediationTasks+1)
	if err != nil {
		return nil, err
	}
	out := make([]RemediationTask, 0, len(tasks))
	users, teams := []string{}, []string{}
	if p.TasksView {
		for _, t := range tasks {
			if t.AssignedUserID != nil {
				users = append(users, *t.AssignedUserID)
			}
			if t.AssignedTeamID != nil {
				teams = append(teams, *t.AssignedTeamID)
			}
		}
	}
	userNames, teamNames := map[string]string{}, map[string]string{}
	if p.TasksView {
		userNames, err = s.names.UserNames(ctx, users)
		if err != nil {
			return nil, err
		}
		teamNames, err = s.names.TeamNames(ctx, teams)
		if err != nil {
			return nil, err
		}
	}
	for _, t := range tasks {
		v := RemediationTask{ID: t.ID, Status: t.Status, DueAt: t.DueAt, AssigneeHidden: !p.TasksView}
		if p.TasksView {
			v.AssignedUserID = t.AssignedUserID
			v.AssignedTeamID = t.AssignedTeamID
			if t.AssignedUserID != nil {
				if n := userNames[*t.AssignedUserID]; n != "" {
					v.AssigneeName = &n
				}
			} else if t.AssignedTeamID != nil {
				if n := teamNames[*t.AssignedTeamID]; n != "" {
					v.AssigneeName = &n
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func advisoryNode(id string) relationships.Node { return relationships.Node{Type: "advisory", ID: id} }
func changeNode(id string) relationships.Node   { return relationships.Node{Type: "change", ID: id} }

// LinkChange validates the target through Changes' public reader. The parent
// lock serializes the cap; Graph.Link handles concurrent duplicate inserts.
func (s *Service) LinkChange(ctx context.Context, c Caller, p Principal, id, changeID string, expected *int) (ChangeLink, error) {
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return ChangeLink{}, ErrForbidden
	}
	if err := c.validate(); err != nil {
		return ChangeLink{}, err
	}
	version, err := requireVersion(expected)
	if err != nil {
		return ChangeLink{}, err
	}
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(changeID) {
		return ChangeLink{}, ErrNotFound
	}
	found, err := s.changes.Lookup(ctx, []string{changeID}, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return ChangeLink{}, err
	}
	if _, ok := found[changeID]; !ok {
		return ChangeLink{}, ErrNotFound
	}
	if !canSeeChange(p, found[changeID]) {
		return ChangeLink{}, ErrNotFound
	}
	var out ChangeLink
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		parent, e := s.lockAdvisory(ctx, tx, id, version, "link_change")
		if e != nil {
			return e
		}
		page, e := s.graph.Outgoing(ctx, tx, advisoryNode(id), []string{"REMEDIATED_BY"}, "", MaxRemediationChanges+1)
		if e != nil {
			return e
		}
		for _, link := range page.Items {
			if link.Target.ID == changeID {
				out = ChangeLink{ID: link.ID, ChangeID: changeID, Reference: found[changeID].Reference, Status: found[changeID].Status}
				return nil
			}
		}
		if len(page.Items) >= MaxRemediationChanges {
			return invalid("linked change limit reached")
		}
		rel, created, e := s.graph.Link(ctx, tx, relationships.LinkInput{Owner: "security", Source: advisoryNode(id), Type: "REMEDIATED_BY", Target: changeNode(changeID), Confidence: relationships.ConfidenceDeclared, CreatedBy: c.Actor.UserID, RecordedBy: "security"})
		if e != nil {
			return e
		}
		out = ChangeLink{ID: rel.ID, ChangeID: changeID, Reference: found[changeID].Reference, Status: found[changeID].Status}
		if created {
			if _, e = s.store.UpdateAdvisoryTx(ctx, tx, parent); e != nil {
				return e
			}
			return recordAudit(ctx, tx, c, "security.change.linked", "advisory", id, nil, map[string]any{"changeId": changeID}, nil)
		}
		return nil
	})
	return out, err
}

func (s *Service) UnlinkChange(ctx context.Context, c Caller, p Principal, id, changeID string, expected *int) error {
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return ErrForbidden
	}
	if err := c.validate(); err != nil {
		return err
	}
	version, err := requireVersion(expected)
	if err != nil {
		return err
	}
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(changeID) {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		parent, e := s.lockAdvisory(ctx, tx, id, version, "unlink_change")
		if e != nil {
			return e
		}
		ended, e := s.graph.UnlinkTriple(ctx, tx, "security", advisoryNode(id), "REMEDIATED_BY", changeNode(changeID), "removed", c.Actor.UserID)
		if e != nil {
			return e
		}
		if ended {
			if _, e = s.store.UpdateAdvisoryTx(ctx, tx, parent); e != nil {
				return e
			}
			return recordAudit(ctx, tx, c, "security.change.unlinked", "advisory", id, map[string]any{"changeId": changeID}, nil, nil)
		}
		return nil
	})
}

func (s *Service) LinkedChanges(ctx context.Context, p Principal, id string) ([]ChangeLink, error) {
	if !p.reads() {
		return nil, ErrForbidden
	}
	if _, err := s.checkObject(ctx, "advisory", id); err != nil {
		return nil, err
	}
	page, err := s.graph.Outgoing(ctx, s.relationshipsDB, advisoryNode(id), []string{"REMEDIATED_BY"}, "", MaxRemediationChanges+1)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, r := range page.Items {
		ids = append(ids, r.Target.ID)
	}
	found, err := s.changes.Lookup(ctx, ids, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return nil, err
	}
	out := make([]ChangeLink, 0, len(page.Items))
	for i, r := range page.Items {
		v, visible := found[r.Target.ID]
		link := ChangeLink{ID: r.ID, Hidden: !visible || !canSeeChange(p, v)}
		if !link.Hidden {
			if ok := visible; ok {
				link.ChangeID = v.ID
				link.Reference = v.Reference
				link.Status = v.Status
			} else {
				link.Hidden = true
			}
		}
		if link.Hidden {
			link.ID = fmt.Sprintf("hidden-%d", i+1)
		}
		out = append(out, link)
	}
	return out, nil
}

func canSeeChange(p Principal, c changespublic.ChangeInfo) bool {
	return p.ChangesView || c.RequesterID == p.UserID || c.OwnerID != nil && *c.OwnerID == p.UserID
}
