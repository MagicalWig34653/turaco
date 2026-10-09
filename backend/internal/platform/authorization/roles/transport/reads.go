package transport

import (
	"net/http"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// permissionModule names the module a permission belongs to (the picker groups by it). It follows the permission
// prefix; prefixes of one module share a group.
func permissionModule(name string) string {
	prefix, _, _ := strings.Cut(name, ".")
	switch prefix {
	case "platform", "views":
		return "platform"
	case "modules":
		return "access"
	case "endpoint", "endpoints", "integrations", "software", "deployments":
		return "endpoints"
	case "tickets", "servicedesk", "majorincidents", "problems":
		return "servicedesk"
	case "runbooks":
		return "knowledge"
	case "remote_access":
		return "remoteaccess"
	default:
		return prefix
	}
}

// permissionGroup classifies a permission by what it allows: view, manage, execute, approve, admin or other.
func permissionGroup(name string) string {
	i := strings.LastIndexByte(name, '.')
	switch last := name[i+1:]; last {
	case "view", "manage", "execute", "approve", "admin":
		return last
	}
	return "other"
}

type templateRoleDTO struct {
	RoleID          string   `json:"roleId"`
	RoleKey         string   `json:"roleKey"`
	Name            string   `json:"name"`
	TemplateVersion int      `json:"templateVersion"`
	Missing         []string `json:"missing"`
	Extra           []string `json:"extra"`
}

type templateDTO struct {
	Key                     string            `json:"key"`
	Version                 int               `json:"version"`
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	NameKey                 string            `json:"nameKey"`
	DescriptionKey          string            `json:"descriptionKey"`
	Audience                string            `json:"audience"`
	Permissions             []string          `json:"permissions"`
	RequiresModules         []string          `json:"requiresModules"`
	ExternalOnly            bool              `json:"externalOnly"`
	AdministratorAssignOnly bool              `json:"administratorAssignOnly"`
	Roles                   []templateRoleDTO `json:"roles"`
}

func (h *httpHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListTemplates(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]templateDTO, 0, len(list))
	for _, t := range list {
		d := templateDTO{Key: t.Key, Version: t.Version, Name: t.Name, Description: t.Description,
			NameKey: "roles.template." + t.Key + ".name", DescriptionKey: "roles.template." + t.Key + ".description",
			Audience: t.Audience, Permissions: t.Permissions, RequiresModules: t.RequiresModules, ExternalOnly: t.ExternalOnly,
			AdministratorAssignOnly: t.AdministratorAssignOnly, Roles: []templateRoleDTO{}}
		if d.RequiresModules == nil {
			d.RequiresModules = []string{}
		}
		for _, r := range t.Roles {
			d.Roles = append(d.Roles, templateRoleDTO{RoleID: r.RoleID, RoleKey: r.RoleKey, Name: r.Name, TemplateVersion: r.TemplateVersion,
				Missing: r.Missing, Extra: r.Extra})
		}
		items = append(items, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *httpHandler) listSoDRules(w http.ResponseWriter, _ *http.Request) {
	type ruleDTO struct {
		Key        string   `json:"key"`
		MessageKey string   `json:"messageKey"`
		Left       []string `json:"left"`
		Right      []string `json:"right"`
	}
	items := []ruleDTO{}
	for _, r := range roles.SoDRules() {
		items = append(items, ruleDTO{Key: r.Key, MessageKey: "roles.sod." + r.Key, Left: r.Left, Right: r.Right})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type grantPathDTO struct {
	RoleID       string     `json:"roleId"`
	RoleKey      string     `json:"roleKey"`
	RoleName     string     `json:"roleName"`
	BuiltInAdmin bool       `json:"builtInAdmin"`
	AssignmentID string     `json:"assignmentId"`
	Source       string     `json:"source"`
	GroupID      string     `json:"groupId,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

func (h *httpHandler) effectivePermissions(w http.ResponseWriter, r *http.Request) {
	ep, err := h.svc.EffectivePermissionsOf(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type permDTO struct {
		Name      string   `json:"name"`
		Risk      string   `json:"risk"`
		Module    string   `json:"module"`
		GrantedBy []string `json:"grantedBy"`
	}
	out := struct {
		UserID       string         `json:"userId"`
		Roles        []grantPathDTO `json:"roles"`
		Permissions  []permDTO      `json:"permissions"`
		Warnings     []string       `json:"warnings"`
		Ceilings     []string       `json:"ceilings"`
		LocalAccount bool           `json:"localAccount"`
		Excluded     []string       `json:"excluded"`
	}{UserID: r.PathValue("id"), Roles: []grantPathDTO{}, Permissions: []permDTO{}, Warnings: []string{}, Ceilings: []string{}, LocalAccount: ep.LocalAccount, Excluded: []string{}}
	for _, g := range ep.Roles {
		out.Roles = append(out.Roles, grantPathDTO{RoleID: g.RoleID, RoleKey: g.RoleKey, RoleName: g.RoleName, BuiltInAdmin: g.BuiltInAdmin,
			AssignmentID: g.AssignmentID, Source: g.Source, GroupID: g.GroupID, ExpiresAt: g.ExpiresAt})
	}
	for _, p := range ep.Permissions {
		by := p.GrantedBy
		if by == nil {
			by = []string{}
		}
		out.Permissions = append(out.Permissions, permDTO{Name: p.Name, Risk: p.Risk, Module: permissionModule(p.Name), GrantedBy: by})
	}
	for _, rule := range ep.Warnings {
		out.Warnings = append(out.Warnings, rule.Key)
	}
	if ep.LocalAccount {
		out.Ceilings = append(out.Ceilings, "local_account_high_risk")
		out.Excluded = append(out.Excluded, ep.Excluded...)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *httpHandler) holders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_limit", "The limit must be a positive integer.")
		return
	}
	res, err := h.svc.Holders(r.Context(), q.Get("permission"), roles.Page{Limit: limit, Cursor: q.Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type holderDTO struct {
		AssignmentID       string     `json:"assignmentId"`
		RoleID             string     `json:"roleId"`
		RoleKey            string     `json:"roleKey"`
		RoleName           string     `json:"roleName"`
		BuiltInAdmin       bool       `json:"builtInAdmin"`
		SubjectType        string     `json:"subjectType"`
		SubjectID          string     `json:"subjectId"`
		SubjectDisplayName string     `json:"subjectDisplayName"`
		ExpiresAt          *time.Time `json:"expiresAt,omitempty"`
	}
	items := make([]holderDTO, 0, len(res.Items))
	for _, x := range res.Items {
		items = append(items, holderDTO{AssignmentID: x.AssignmentID, RoleID: x.RoleID, RoleKey: x.RoleKey, RoleName: x.RoleName,
			BuiltInAdmin: x.BuiltInAdmin, SubjectType: x.SubjectType, SubjectID: x.SubjectID, SubjectDisplayName: x.SubjectDisplayName, ExpiresAt: x.ExpiresAt})
	}
	out := map[string]any{"items": items}
	if res.NextCursor != "" {
		out["nextCursor"] = res.NextCursor
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *httpHandler) roleMembers(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.MembersOf(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type memberDTO struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
		Source      string `json:"source"`
	}
	items := make([]memberDTO, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, memberDTO{UserID: m.UserID, DisplayName: m.DisplayName, Source: m.Source})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "capped": res.Capped, "limit": roles.MaxRoleMembers})
}
