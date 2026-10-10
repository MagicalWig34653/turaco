package intune

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// GraphWriter is the Microsoft Graph implementation of AssignmentWriter (Mode real, status unverified until a
// call succeeded). It uses its own app registration, separate from the read client, and is only wired when
// SOFTWARE_DEPLOY_WRITE is on. It follows the vendor documentation and has not been exercised against a live tenant.
//
// Required application permissions (write registration): DeviceManagementApps.ReadWrite.All (mobileAppAssignment
// create/delete, https://learn.microsoft.com/en-us/graph/api/intune-apps-mobileappassignment-create?view=graph-rest-1.0,
// read 2026-10-10), DeviceManagementManagedDevices.Read.All (resolve the managed device),
// Group.ReadWrite.All (create and find the Turaco-owned group; Group.Create alone cannot read it back,
// https://learn.microsoft.com/en-us/graph/api/group-post-groups?view=graph-rest-1.0, read 2026-10-10),
// GroupMember.ReadWrite.All and Device.ReadWrite.All (add a device to a group,
// https://learn.microsoft.com/en-us/graph/api/group-post-members?view=graph-rest-1.0, read 2026-10-10).
//
// Ownership and idempotency. Graph chooses group ids, so the Turaco-owned group of a ring is identified by its
// display name turaco-ring-<ringId> (RingGroupID); the writer finds it by that name (creating it when missing)
// and never touches another group. Replaying a request leaves the same state: group membership is replaced by
// exactly DeviceExternalIDs (devices only), the ring's assignment of the artifact is created when missing and
// replaced when its intent differs, and other assignments of the artifact are never touched. Clear removes the
// ring's assignment and empties the group; it succeeds when both are already gone.
type GraphWriter struct {
	g  *microsoft.Graph
	mu sync.Mutex
}

var _ AssignmentWriter = (*GraphWriter)(nil)

// NewGraphWriter wraps a Graph client of the write registration.
func NewGraphWriter(g *microsoft.Graph) *GraphWriter { return &GraphWriter{g: g} }

// Status implements providerstatus.Reporter.
func (w *GraphWriter) Status() providerstatus.Snapshot { return w.g.Status() }

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SetRingAssignment implements AssignmentWriter.
func (w *GraphWriter) SetRingAssignment(ctx context.Context, op RingAssignmentOp) error {
	if err := op.Validate(); err != nil {
		return err
	}
	name, err := ringGroupDisplayName(op.RingKey, op.TargetGroupExternalID)
	if err != nil {
		return err
	}
	if !idPattern.MatchString(op.ManagementArtifactExternalID) {
		return fmt.Errorf("%w: artifact id", ErrPermanent)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. The group. Existence of the artifact is checked first so that an unknown artifact creates nothing.
	if _, err := w.assignments(ctx, op.ManagementArtifactExternalID); err != nil {
		return err
	}
	groupID, found, err := w.findGroup(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		if groupID, err = w.createGroup(ctx, name); err != nil {
			return err
		}
	}
	// 2. The members: exactly the requested devices.
	want, err := w.resolveDevices(ctx, op.DeviceExternalIDs)
	if err != nil {
		return err
	}
	have, err := w.members(ctx, groupID)
	if err != nil {
		return err
	}
	for _, id := range want {
		if !have[id] {
			if err := w.addMember(ctx, groupID, id); err != nil {
				return err
			}
		}
	}
	wanted := map[string]bool{}
	for _, id := range want {
		wanted[id] = true
	}
	for id := range have {
		if !wanted[id] {
			if err := w.removeMember(ctx, groupID, id); err != nil {
				return err
			}
		}
	}
	// 3. The assignment.
	return w.ensureAssignment(ctx, op.ManagementArtifactExternalID, groupID, op.Intent)
}

// ClearRingAssignment implements AssignmentWriter.
func (w *GraphWriter) ClearRingAssignment(ctx context.Context, operationID, artifact, ringKey string) error {
	if operationID == "" || artifact == "" || ringKey == "" {
		return fmt.Errorf("%w: operation id, artifact and ring key are required", ErrPermanent)
	}
	name, err := ringGroupDisplayName(ringKey, RingGroupID(ringKey))
	if err != nil {
		return err
	}
	if !idPattern.MatchString(artifact) {
		return fmt.Errorf("%w: artifact id", ErrPermanent)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	groupID, found, err := w.findGroup(ctx, name)
	if err != nil || !found {
		return err
	}
	list, err := w.assignments(ctx, artifact)
	if err != nil && !errors.Is(err, ErrArtifactUnknown) {
		return err
	}
	for _, a := range list {
		if isGroupTarget(a, groupID) {
			if err := w.deleteAssignment(ctx, artifact, a.ID); err != nil {
				return err
			}
		}
	}
	have, err := w.members(ctx, groupID)
	if err != nil {
		return err
	}
	for id := range have {
		if err := w.removeMember(ctx, groupID, id); err != nil {
			return err
		}
	}
	return nil
}

// ringGroupDisplayName validates that the request names a Turaco ring group and returns its display name.
func ringGroupDisplayName(ringKey, group string) (string, error) {
	name := strings.ToLower(RingGroupID(ringKey))
	if !ringGroupName.MatchString(name) || !strings.EqualFold(group, name) {
		return "", fmt.Errorf("%w: the target group is not the Turaco-owned group of the ring", ErrPermanent)
	}
	return name, nil
}

// classify maps client errors to the writer contract: transient (retry with a new attempt) or permanent.
func classify(step string, err error) error {
	if err == nil {
		return nil
	}
	var te *microsoft.TransientError
	switch {
	case errors.Is(err, ErrArtifactUnknown), errors.Is(err, ErrPermanent), errors.Is(err, ErrTransient):
		return err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.As(err, &te):
		return fmt.Errorf("%w: %s: %w", ErrTransient, step, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrPermanent, step, err)
}

// ---- groups ----------------------------------------------------------------------------------------------

// findGroup: GET /groups?$filter=displayName eq '<name>' (https://learn.microsoft.com/en-us/graph/api/group-list?view=graph-rest-1.0,
// read 2026-10-10). The name is validated (turaco-ring-<uuid>) before it reaches the filter.
func (w *GraphWriter) findGroup(ctx context.Context, name string) (string, bool, error) {
	var ids []string
	err := w.g.List(ctx, query("/v1.0/groups", "$filter", "displayName eq '"+name+"'", "$select", "id,displayName"), nil, func(raw json.RawMessage) error {
		var g struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal(raw, &g) == nil && g.ID != "" && strings.EqualFold(g.DisplayName, name) {
			ids = append(ids, g.ID)
		}
		return nil
	})
	if err != nil {
		return "", false, classify("find group", err)
	}
	switch len(ids) {
	case 0:
		return "", false, nil
	case 1:
		if !guidPattern.MatchString(ids[0]) {
			return "", false, fmt.Errorf("%w: find group: unexpected group id", ErrPermanent)
		}
		return ids[0], true, nil
	}
	return "", false, fmt.Errorf("%w: find group: the group name is ambiguous", ErrPermanent)
}

// createGroup: POST /groups (security group, assigned membership), body per
// https://learn.microsoft.com/en-us/graph/api/group-post-groups?view=graph-rest-1.0 (read 2026-10-10).
func (w *GraphWriter) createGroup(ctx context.Context, name string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	body := map[string]any{
		"displayName": name, "mailNickname": name, "description": "Owned by Turaco: deployment ring group. Membership is managed automatically.",
		"mailEnabled": false, "securityEnabled": true, "groupTypes": []string{},
	}
	if _, err := w.g.Call(ctx, microsoft.Request{Method: http.MethodPost, Target: "/v1.0/groups", Body: body}, &out); err != nil {
		return "", classify("create group", err)
	}
	if !guidPattern.MatchString(out.ID) {
		return "", fmt.Errorf("%w: create group: unexpected group id", ErrPermanent)
	}
	return out.ID, nil
}

// members lists the device members of the group (direct members only; the group is Turaco-owned).
func (w *GraphWriter) members(ctx context.Context, groupID string) (map[string]bool, error) {
	out := map[string]bool{}
	err := w.g.List(ctx, query("/v1.0/groups/"+url.PathEscape(groupID)+"/members/microsoft.graph.device", "$select", "id", "$top", "999"),
		http.Header{"ConsistencyLevel": {"eventual"}}, func(raw json.RawMessage) error {
			var m struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &m) == nil && m.ID != "" {
				out[strings.ToLower(m.ID)] = true
			}
			return nil
		})
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		// Just created and not replicated yet.
		return nil, fmt.Errorf("%w: list members: group not yet visible", ErrTransient)
	}
	return out, classify("list members", err)
}

func (w *GraphWriter) addMember(ctx context.Context, groupID, deviceObjectID string) error {
	body := map[string]string{"@odata.id": microsoft.GraphBaseURL + "/v1.0/directoryObjects/" + deviceObjectID}
	_, err := w.g.Call(ctx, microsoft.Request{Method: http.MethodPost, Target: "/v1.0/groups/" + url.PathEscape(groupID) + "/members/$ref", Body: body}, nil)
	if err == nil {
		return nil
	}
	// 400 means "already a member" or "group created moments ago and not replicated yet" (documented on the Add
	// members page). Read the members again: present is success, absent is worth another attempt.
	if microsoft.IsGraphStatus(err, http.StatusBadRequest) || microsoft.IsGraphStatus(err, http.StatusNotFound) {
		have, lerr := w.members(ctx, groupID)
		if lerr == nil && have[strings.ToLower(deviceObjectID)] {
			return nil
		}
		return fmt.Errorf("%w: add member: not accepted yet", ErrTransient)
	}
	return classify("add member", err)
}

func (w *GraphWriter) removeMember(ctx context.Context, groupID, deviceObjectID string) error {
	// DELETE .../members/{id}/$ref removes the membership only; without $ref the directory object would be deleted
	// (https://learn.microsoft.com/en-us/graph/api/group-delete-members?view=graph-rest-1.0, read 2026-10-10).
	_, err := w.g.Call(ctx, microsoft.Request{Method: http.MethodDelete,
		Target: "/v1.0/groups/" + url.PathEscape(groupID) + "/members/" + url.PathEscape(deviceObjectID) + "/$ref"}, nil)
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		return nil
	}
	return classify("remove member", err)
}

// resolveDevices maps managed device ids to the Entra device object ids that group membership needs, rejecting
// ids the provider does not know instead of ignoring them.
// GET /deviceManagement/managedDevices/{id} (azureADDeviceId), then GET /devices?$filter=deviceId eq '<id>'
// (https://learn.microsoft.com/en-us/graph/api/device-list?view=graph-rest-1.0, read 2026-10-10; Device.Read.All).
func (w *GraphWriter) resolveDevices(ctx context.Context, managedIDs []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, mid := range managedIDs {
		if !idPattern.MatchString(mid) {
			return nil, fmt.Errorf("%w: device id", ErrPermanent)
		}
		var md struct {
			AzureADDeviceID string `json:"azureADDeviceId"`
		}
		if err := w.g.Get(ctx, query("/v1.0/deviceManagement/managedDevices/"+url.PathEscape(mid), "$select", "id,azureADDeviceId"), &md); err != nil {
			if microsoft.IsGraphStatus(err, http.StatusNotFound) {
				return nil, fmt.Errorf("%w: a device is not known to the provider", ErrPermanent)
			}
			return nil, classify("resolve device", err)
		}
		aad := strings.ToLower(strings.TrimSpace(md.AzureADDeviceID))
		if !guidPattern.MatchString(aad) || aad == "00000000-0000-0000-0000-000000000000" {
			return nil, fmt.Errorf("%w: a device has no Entra device id", ErrPermanent)
		}
		var ids []string
		err := w.g.List(ctx, query("/v1.0/devices", "$filter", "deviceId eq '"+aad+"'", "$select", "id,deviceId"), nil, func(raw json.RawMessage) error {
			var d struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &d) == nil && guidPattern.MatchString(d.ID) {
				ids = append(ids, strings.ToLower(d.ID))
			}
			return nil
		})
		if err != nil {
			return nil, classify("resolve device object", err)
		}
		if len(ids) != 1 {
			return nil, fmt.Errorf("%w: a device has no unique Entra device object", ErrPermanent)
		}
		if !seen[ids[0]] {
			seen[ids[0]] = true
			out = append(out, ids[0])
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---- assignments -----------------------------------------------------------------------------------------

// assignments lists the assignments of an application: GET /deviceAppManagement/mobileApps/{id}/assignments
// (https://learn.microsoft.com/en-us/graph/api/intune-apps-mobileappassignment-list?view=graph-rest-1.0, read
// 2026-10-10). A 404 means the artifact is unknown.
func (w *GraphWriter) assignments(ctx context.Context, artifact string) ([]graphAssignment, error) {
	var out []graphAssignment
	err := w.g.List(ctx, "/v1.0/deviceAppManagement/mobileApps/"+url.PathEscape(artifact)+"/assignments", nil, func(raw json.RawMessage) error {
		var a graphAssignment
		if json.Unmarshal(raw, &a) == nil && a.ID != "" {
			out = append(out, a)
		}
		return nil
	})
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		return nil, ErrArtifactUnknown
	}
	return out, classify("list assignments", err)
}

func isGroupTarget(a graphAssignment, groupID string) bool {
	return strings.TrimPrefix(strings.ToLower(a.Target.ODataType), "#") == "microsoft.graph.groupassignmenttarget" &&
		strings.EqualFold(a.Target.GroupID, groupID)
}

// ensureAssignment makes exactly one include assignment of the artifact to the group with the intent. Other
// assignments (other groups, exclusions, All devices) are never touched.
func (w *GraphWriter) ensureAssignment(ctx context.Context, artifact, groupID, intent string) error {
	list, err := w.assignments(ctx, artifact)
	if err != nil {
		return err
	}
	kept := false
	for _, a := range list {
		if !isGroupTarget(a, groupID) {
			continue
		}
		if !kept && strings.EqualFold(a.Intent, intent) {
			kept = true
			continue
		}
		if err := w.deleteAssignment(ctx, artifact, a.ID); err != nil {
			return err
		}
	}
	if kept {
		return nil
	}
	// POST /deviceAppManagement/mobileApps/{id}/assignments, 201 Created (create mobileAppAssignment page above).
	body := map[string]any{
		"@odata.type": "#microsoft.graph.mobileAppAssignment",
		"intent":      intent,
		"target":      map[string]any{"@odata.type": "#microsoft.graph.groupAssignmentTarget", "groupId": groupID},
	}
	_, err = w.g.Call(ctx, microsoft.Request{Method: http.MethodPost, Target: "/v1.0/deviceAppManagement/mobileApps/" + url.PathEscape(artifact) + "/assignments", Body: body}, nil)
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		return ErrArtifactUnknown
	}
	return classify("create assignment", err)
}

func (w *GraphWriter) deleteAssignment(ctx context.Context, artifact, assignmentID string) error {
	if !idPattern.MatchString(assignmentID) {
		return fmt.Errorf("%w: assignment id", ErrPermanent)
	}
	_, err := w.g.Call(ctx, microsoft.Request{Method: http.MethodDelete,
		Target: "/v1.0/deviceAppManagement/mobileApps/" + url.PathEscape(artifact) + "/assignments/" + url.PathEscape(assignmentID)}, nil)
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		return nil
	}
	return classify("delete assignment", err)
}
