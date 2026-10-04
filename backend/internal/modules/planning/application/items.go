package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// ItemLink is one record an Initiative includes.
type ItemLink struct {
	RelationshipID string
	Type           string
	ID             string
	Since          time.Time
}

// checkItem verifies that a record exists and can be included and returns its
// normalized id. A record of a type the caller may not see is
// ErrReferenceInvalid without any lookup, so adding never confirms that hidden
// records exist (the rule of Change affected resources).
func (s *Service) checkItem(ctx context.Context, p Principal, itemType, itemID string) (string, error) {
	if !oneOf(itemType, ItemTypes) {
		return "", invalid("type must be one of %s", strings.Join(ItemTypes, ", "))
	}
	id, err := checkID(itemID)
	if err != nil {
		return "", err
	}
	if p.hides(itemType) {
		return "", ErrReferenceInvalid
	}
	usable := false
	switch itemType {
	case NodeChange:
		found, err := s.changes.Lookup(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check change: %w", err)
		}
		c, ok := found[id]
		usable = ok && c.Status != "cancelled" && c.Status != "rejected"
	case NodeTask:
		found, err := s.tasks.Tasks(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check task: %w", err)
		}
		usable = len(found) == 1 && found[0].ID == id && found[0].Status != "cancelled"
	case NodeProcurementRequest:
		found, err := s.procurement.Requests(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check procurement request: %w", err)
		}
		r, ok := found[id]
		usable = ok && r.Status != "cancelled"
	case NodeService:
		found, err := s.services.Lookup(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check service: %w", err)
		}
		v, ok := found[id]
		usable = ok && v.Status != "retired"
	}
	if !usable {
		return "", ErrReferenceInvalid
	}
	return id, nil
}

// AddItem records that an editable Initiative includes a Change, Task,
// Procurement Request or Service. The link is a declared platform Relationship
// (initiative INCLUDES ...); adding an existing link returns it (created is
// false). At most MaxItems per Initiative. Requires planning.manage and
// expectedVersion; the version is bumped when a link is created.
func (s *Service) AddItem(ctx context.Context, c Caller, p Principal, initiativeID string, expected *int, itemType, itemID string) (l ItemLink, created bool, err error) {
	if err := c.validate(); err != nil {
		return ItemLink{}, false, err
	}
	if !p.Manage {
		return ItemLink{}, false, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return ItemLink{}, false, err
	}
	if !uuidPattern.MatchString(initiativeID) {
		return ItemLink{}, false, ErrNotFound
	}
	initiativeID = strings.ToLower(initiativeID)
	itemID, err = s.checkItem(ctx, p, itemType, itemID)
	if err != nil {
		return ItemLink{}, false, err
	}
	src := relationships.Node{Type: NodeInitiative, ID: initiativeID}
	dst := relationships.Node{Type: itemType, ID: itemID}
	var rel relationships.Relationship
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockEditable(ctx, tx, initiativeID, &exp, "add_item")
		if err != nil {
			return err
		}
		page, err := s.graph.Outgoing(ctx, tx, src, []string{RelIncludes}, "", MaxItems+1)
		if err != nil {
			return fmt.Errorf("count included records: %w", err)
		}
		exists := false
		for _, r := range page.Items {
			if r.Target == dst {
				exists = true
			}
		}
		if !exists && len(page.Items) >= MaxItems {
			return ErrTooMany
		}
		rel, created, err = s.graph.Link(ctx, tx, relationships.LinkInput{
			Owner: RelationshipOwner, Source: src, Type: RelIncludes, Target: dst, Confidence: relationships.ConfidenceDeclared,
			CreatedBy: c.Actor.UserID, RecordedBy: "planning",
		})
		if err != nil {
			return fmt.Errorf("link included record: %w", err)
		}
		if !created {
			return nil
		}
		if _, err := s.store.UpdateTx(ctx, tx, addEditor(cur, c.Actor.UserID)); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "item_added", initiativeID, nil, nil,
			map[string]any{"relationshipId": rel.ID, "itemType": itemType, "itemId": itemID})
	})
	if err != nil {
		return ItemLink{}, false, err
	}
	return ItemLink{RelationshipID: rel.ID, Type: itemType, ID: itemID, Since: rel.ValidFrom}, created, nil
}

// RemoveItem ends the link between an editable Initiative and an included
// record. Removing a link that does not exist succeeds without an audit entry.
// A record of a type the caller may not see is ErrReferenceInvalid, as when
// adding. Requires planning.manage and expectedVersion.
func (s *Service) RemoveItem(ctx context.Context, c Caller, p Principal, initiativeID string, expected *int, itemType, itemID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return err
	}
	if !uuidPattern.MatchString(initiativeID) {
		return ErrNotFound
	}
	if !oneOf(itemType, ItemTypes) {
		return invalid("type must be one of %s", strings.Join(ItemTypes, ", "))
	}
	if itemID, err = checkID(itemID); err != nil {
		return err
	}
	if p.hides(itemType) {
		return ErrReferenceInvalid
	}
	initiativeID = strings.ToLower(initiativeID)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockEditable(ctx, tx, initiativeID, &exp, "remove_item")
		if err != nil {
			return err
		}
		ended, err := s.graph.UnlinkTriple(ctx, tx, RelationshipOwner, relationships.Node{Type: NodeInitiative, ID: initiativeID}, RelIncludes,
			relationships.Node{Type: itemType, ID: itemID}, reasonRemoved, c.Actor.UserID)
		if err != nil {
			return fmt.Errorf("unlink included record: %w", err)
		}
		if !ended {
			return nil
		}
		if _, err := s.store.UpdateTx(ctx, tx, addEditor(cur, c.Actor.UserID)); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "item_removed", initiativeID, nil, nil, map[string]any{"itemType": itemType, "itemId": itemID})
	})
}
