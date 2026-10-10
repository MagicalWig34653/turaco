package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

const (
	permUsersManage       = "organization.users.manage"
	permLocationsManage   = "organization.locations.manage"
	permDepartmentsManage = "organization.departments.manage"
	permExternalParties   = "organization.external_parties.manage"
	permPlatformAdmin     = "platform.admin"
	maxPeopleBody         = 8 << 10
)

type peopleHandler struct {
	people  *application.People
	queries *application.Queries
	reader  application.Reader
	logger  *slog.Logger
}

// RegisterPeople mounts the People administration routes (F14): User, Location and Department write operations,
// the Field Catalogs and query endpoints of Users, Teams, Locations and Departments, and the Department reads.
// Every route requires authentication and the named permission; the backend, not the UI, is the authority.
func RegisterPeople(mux *http.ServeMux, people *application.People, queries *application.Queries, reader application.Reader,
	auth authorization.Authenticator, logger *slog.Logger) {
	h := &peopleHandler{people: people, queries: queries, reader: reader, logger: logger}
	view := authorization.Require(auth, permView)
	users := authorization.Require(auth, permUsersManage)
	locs := authorization.Require(auth, permLocationsManage)
	depts := authorization.Require(auth, permDepartmentsManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	// Literal patterns, so the route-family test of the module registry sees them.
	route("GET /api/v1/users/fields", view, func(w http.ResponseWriter, r *http.Request) { h.fields(w, r, "users") })
	route("POST /api/v1/users/query", view, func(w http.ResponseWriter, r *http.Request) { h.query(w, r, "users") })
	route("GET /api/v1/teams/fields", view, func(w http.ResponseWriter, r *http.Request) { h.fields(w, r, "teams") })
	route("POST /api/v1/teams/query", view, func(w http.ResponseWriter, r *http.Request) { h.query(w, r, "teams") })
	route("GET /api/v1/locations/fields", view, func(w http.ResponseWriter, r *http.Request) { h.fields(w, r, "locations") })
	route("POST /api/v1/locations/query", view, func(w http.ResponseWriter, r *http.Request) { h.query(w, r, "locations") })
	route("GET /api/v1/departments/fields", view, func(w http.ResponseWriter, r *http.Request) { h.fields(w, r, "departments") })
	route("POST /api/v1/departments/query", view, func(w http.ResponseWriter, r *http.Request) { h.query(w, r, "departments") })
	route("GET /api/v1/departments", view, h.listDepartments)
	route("GET /api/v1/departments/{id}", view, h.getDepartment)

	route("POST /api/v1/users", users, h.createUser)
	route("PATCH /api/v1/users/{id}/profile", users, h.updateProfile)
	route("PUT /api/v1/users/{id}/department", users, h.setDepartment)
	route("PUT /api/v1/users/{id}/primary-location", users, h.setLocation)
	route("PUT /api/v1/users/{id}/manager", users, h.setManager)
	route("POST /api/v1/users/{id}/deactivate", users, h.status(application.OpDeactivate))
	route("POST /api/v1/users/{id}/reactivate", users, h.status(application.OpReactivate))
	route("POST /api/v1/users/{id}/mark-departed", users, h.status(application.OpMarkDeparted))
	route("POST /api/v1/users/{id}/send-invitation", users, h.sendInvitation)
	route("POST /api/v1/users/{id}/reset-password", users, h.resetPassword)

	route("POST /api/v1/locations", locs, h.createLocation)
	route("PATCH /api/v1/locations/{id}", locs, h.updateLocation)
	route("POST /api/v1/locations/{id}/move", locs, h.moveLocation)
	route("POST /api/v1/locations/{id}/deactivate", locs, h.locationActive(false))
	route("POST /api/v1/locations/{id}/activate", locs, h.locationActive(true))

	route("POST /api/v1/departments", depts, h.createDepartment)
	route("PATCH /api/v1/departments/{id}", depts, h.updateDepartment)
	route("POST /api/v1/departments/{id}/move", depts, h.moveDepartment)
	route("POST /api/v1/departments/{id}/deactivate", depts, h.departmentActive(false))
	route("POST /api/v1/departments/{id}/activate", depts, h.departmentActive(true))
}

// failPeople maps the people errors to API errors; the message is a fixed, user-safe text.
func failPeople(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var owned *application.FieldDirectoryOwnedError
	var impact *application.ImpactError
	switch {
	case query.WriteError(w, err):
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", inv.Message)
	case errors.As(err, &owned):
		httpx.WriteErrorDetails(w, http.StatusConflict, "organization.field_directory_owned",
			"These attributes are maintained by the directory and cannot be changed here.", map[string]any{"fields": owned.Fields})
	case errors.As(err, &impact):
		httpx.WriteErrorDetails(w, http.StatusConflict, "organization.impact_confirmation_required",
			"Other records still refer to this record; repeat the request with confirmImpact to continue.", map[string]any{"counts": impact.Counts})
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "organization.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "organization.conflict", "The request conflicts with the current state.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "organization.version_conflict", "The record changed since it was loaded.")
	case errors.Is(err, application.ErrDirectoryUser):
		httpx.WriteError(w, http.StatusConflict, "organization.directory_user", "This user signs in through the directory.")
	case errors.Is(err, application.ErrEmergencyAccount):
		httpx.WriteError(w, http.StatusConflict, "organization.emergency_account", "Emergency accounts are managed with the command line tool.")
	case errors.Is(err, application.ErrLastAdministrator):
		httpx.WriteError(w, http.StatusConflict, "organization.last_administrator", "This would leave the platform without an active administrator.")
	case errors.Is(err, application.ErrSelfOperation):
		httpx.WriteError(w, http.StatusConflict, "organization.self_operation", "You cannot do this to your own account.")
	case errors.Is(err, application.ErrTeamMembershipSelf):
		httpx.WriteError(w, http.StatusConflict, "access.team_membership_self", "You cannot change your own team membership.")
	case errors.Is(err, application.ErrDominanceRequired):
		httpx.WriteError(w, http.StatusForbidden, "access.dominance_required", "This account holds permissions that you do not hold; ask a platform administrator.")
	case errors.Is(err, application.ErrExternalNeedsPermission):
		httpx.WriteError(w, http.StatusForbidden, "access.external_party_permission_required", "Adding an external account needs the external parties permission.")
	case errors.Is(err, application.ErrDirectoryIdentityDisabled):
		httpx.WriteError(w, http.StatusConflict, "organization.directory_identity_disabled", "The directory account is disabled; the user stays inactive until the directory enables it.")
	case errors.Is(err, application.ErrWrongState):
		httpx.WriteError(w, http.StatusConflict, "organization.invalid_state", "The operation does not apply to the current state.")
	case errors.Is(err, application.ErrHierarchy):
		httpx.WriteError(w, http.StatusConflict, "organization.invalid_hierarchy", "The change would create a cycle or exceed the maximum depth.")
	case errors.Is(err, application.ErrTargetInactive):
		httpx.WriteError(w, http.StatusConflict, "organization.target_inactive", "The referenced record is not active.")
	case errors.Is(err, application.ErrUserNotActive):
		httpx.WriteError(w, http.StatusConflict, "organization.user_not_active", "Only active users can be added to a team.")
	case errors.Is(err, application.ErrTeamInactive):
		httpx.WriteError(w, http.StatusConflict, "organization.team_inactive", "Members can only be added to an active team.")
	case errors.Is(err, application.ErrMailNotConfigured):
		httpx.WriteError(w, http.StatusConflict, "auth.mail_not_configured", "Email is not configured, so a password reset cannot be delivered.")
	case errors.Is(err, application.ErrBaseURLNotConfigured):
		httpx.WriteError(w, http.StatusConflict, "auth.base_url_not_configured", "The application address (EMAIL_BASE_URL) is not configured.")
	case errors.Is(err, application.ErrNoEmail):
		httpx.WriteError(w, http.StatusConflict, "organization.no_email", "The user has no email address.")
	case errors.Is(err, application.ErrMailFailed):
		httpx.WriteError(w, http.StatusBadGateway, "auth.mail_failed", "The email could not be sent.")
	default:
		logger.ErrorContext(r.Context(), "organization people request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func (h *peopleHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failPeople(h.logger, w, r, err)
}

func peopleDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxPeopleBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func queryPrincipal(r *http.Request) application.QueryPrincipal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.QueryPrincipal{UserID: p.UserID, ViewDetails: p.Has(permViewDetails)}
}

func (h *peopleHandler) fields(w http.ResponseWriter, r *http.Request, resource string) {
	info, err := h.queries.Fields(queryPrincipal(r), resource)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}

// userListDTO is the user in lists: no HR fields without organization.users.view_details.
type userListDTO struct {
	ID           string  `json:"id"`
	DisplayName  string  `json:"displayName"`
	GivenName    *string `json:"givenName"`
	FamilyName   *string `json:"familyName"`
	PrimaryEmail *string `json:"primaryEmail"`
	Status       string  `json:"status"`
	StatusSource string  `json:"statusSource"`
	AccountKind  string  `json:"accountKind"`
	Source       string  `json:"source"`
	Version      int     `json:"version"`
	UpdatedAt    string  `json:"updatedAt"`

	DepartmentID      *string `json:"departmentId,omitempty"`
	PrimaryLocationID *string `json:"primaryLocationId,omitempty"`
	ManagerUserID     *string `json:"managerUserId,omitempty"`
	EmployeeNumber    *string `json:"employeeNumber,omitempty"`
	AccessExpiresAt   *string `json:"accessExpiresAt,omitempty"`
}

func (h *peopleHandler) query(w http.ResponseWriter, r *http.Request, resource string) {
	req, err := query.DecodeBody(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := queryPrincipal(r)
	switch resource {
	case "users":
		page, err := h.queries.Users(r.Context(), p, req)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, func(u application.User) userListDTO {
			d := userListDTO{ID: u.ID, DisplayName: u.DisplayName, GivenName: u.GivenName, FamilyName: u.FamilyName, PrimaryEmail: u.PrimaryEmail,
				Status: u.Status, StatusSource: u.StatusSource, AccountKind: u.AccountKind, Source: u.Origin, Version: u.Version, UpdatedAt: ts(u.UpdatedAt)}
			if p.ViewDetails {
				d.DepartmentID, d.PrimaryLocationID, d.ManagerUserID, d.EmployeeNumber = u.DepartmentID, u.PrimaryLocationID, u.ManagerUserID, u.EmployeeNumber
				d.AccessExpiresAt = tsPtr(u.AccessExpiresAt)
			}
			return d
		}))
	case "teams":
		page, err := h.queries.Teams(r.Context(), p, req)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, toTeam))
	case "locations":
		page, err := h.queries.Locations(r.Context(), p, req)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, toLocation))
	case "departments":
		page, err := h.queries.Departments(r.Context(), p, req)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, toDepartment))
	}
}

func (h *peopleHandler) listDepartments(w http.ResponseWriter, r *http.Request) {
	f, valid := parseNameFilter(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListDepartments(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toDepartment))
}

func (h *peopleHandler) getDepartment(w http.ResponseWriter, r *http.Request) {
	d, err := h.reader.GetDepartment(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toDepartment(d))
}

// ---- users ----

func (h *peopleHandler) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName       string  `json:"displayName"`
		GivenName         *string `json:"givenName"`
		FamilyName        *string `json:"familyName"`
		PrimaryEmail      *string `json:"primaryEmail"`
		EmployeeNumber    *string `json:"employeeNumber"`
		DepartmentID      *string `json:"departmentId"`
		PrimaryLocationID *string `json:"primaryLocationId"`
		AccountKind       string  `json:"accountKind"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	u, err := h.people.CreateUser(r.Context(), caller(w, r), application.NewUserInput{DisplayName: body.DisplayName, GivenName: body.GivenName,
		FamilyName: body.FamilyName, PrimaryEmail: body.PrimaryEmail, EmployeeNumber: body.EmployeeNumber, DepartmentID: body.DepartmentID,
		LocationID: body.PrimaryLocationID, AccountKind: body.AccountKind})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toUser(u))
}

func (h *peopleHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int                   `json:"expectedVersion"`
		DisplayName     application.OptString `json:"displayName"`
		GivenName       application.OptString `json:"givenName"`
		FamilyName      application.OptString `json:"familyName"`
		PrimaryEmail    application.OptString `json:"primaryEmail"`
		EmployeeNumber  application.OptString `json:"employeeNumber"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	u, err := h.people.UpdateProfile(r.Context(), caller(w, r), r.PathValue("id"), application.ProfileChange{ExpectedVersion: body.ExpectedVersion,
		DisplayName: body.DisplayName, GivenName: body.GivenName, FamilyName: body.FamilyName, PrimaryEmail: body.PrimaryEmail, EmployeeNumber: body.EmployeeNumber})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toUser(u))
}

func (h *peopleHandler) link(set func(r *http.Request, w http.ResponseWriter, id string, version int, target *string) (application.User, error), key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if !peopleDecode(w, r, &raw) {
			return
		}
		version, target, valid := parseLinkBody(raw, key)
		if !valid {
			httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "expectedVersion and "+key+" (a UUID or null) are required.")
			return
		}
		u, err := set(r, w, r.PathValue("id"), version, target)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		ok(w, toUser(u))
	}
}

// parseLinkBody reads {"expectedVersion": n, key: "<uuid>" | null}; key must be present.
func parseLinkBody(raw map[string]any, key string) (version int, target *string, valid bool) {
	if len(raw) != 2 {
		return 0, nil, false
	}
	v, okv := raw["expectedVersion"].(float64)
	t, hasKey := raw[key]
	if !okv || !hasKey || v < 1 || v != float64(int(v)) {
		return 0, nil, false
	}
	if t == nil {
		return int(v), nil, true
	}
	s, isString := t.(string)
	if !isString {
		return 0, nil, false
	}
	return int(v), &s, true
}

func (h *peopleHandler) setDepartment(w http.ResponseWriter, r *http.Request) {
	h.link(func(r *http.Request, w http.ResponseWriter, id string, v int, t *string) (application.User, error) {
		return h.people.SetDepartment(r.Context(), caller(w, r), id, v, t)
	}, "departmentId")(w, r)
}

func (h *peopleHandler) setLocation(w http.ResponseWriter, r *http.Request) {
	h.link(func(r *http.Request, w http.ResponseWriter, id string, v int, t *string) (application.User, error) {
		return h.people.SetPrimaryLocation(r.Context(), caller(w, r), id, v, t)
	}, "locationId")(w, r)
}

func (h *peopleHandler) setManager(w http.ResponseWriter, r *http.Request) {
	h.link(func(r *http.Request, w http.ResponseWriter, id string, v int, t *string) (application.User, error) {
		return h.people.SetManager(r.Context(), caller(w, r), id, v, t)
	}, "managerUserId")(w, r)
}

func (h *peopleHandler) status(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedVersion int    `json:"expectedVersion"`
			Reason          string `json:"reason"`
		}
		if !peopleDecode(w, r, &body) {
			return
		}
		u, err := h.people.ChangeStatus(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, op, body.Reason)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		ok(w, toUser(u))
	}
}

type credentialLinkDTO struct {
	ExpiresAt string `json:"expiresAt"`
	Mailed    bool   `json:"mailed"`
	// Link is present only for the invitation of a never-activated account when no mail channel exists. It is shown
	// once and never stored.
	Link string `json:"link,omitempty"`
}

func (h *peopleHandler) sendInvitation(w http.ResponseWriter, r *http.Request) {
	res, err := h.people.SendInvitation(r.Context(), caller(w, r), r.PathValue("id"))
	h.credentialResult(w, r, res, err)
}

func (h *peopleHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	res, err := h.people.ResetPassword(r.Context(), caller(w, r), r.PathValue("id"))
	h.credentialResult(w, r, res, err)
}

func (h *peopleHandler) credentialResult(w http.ResponseWriter, r *http.Request, res application.CredentialLink, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The link is a bearer secret: never cacheable (NoStore is set by the route) and no referrer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	httpx.JSON(w, http.StatusOK, credentialLinkDTO{ExpiresAt: ts(res.ExpiresAt), Mailed: res.Mailed, Link: res.Link})
}

// ---- locations ----

func (h *peopleHandler) createLocation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind        string  `json:"kind"`
		ParentID    *string `json:"parentId"`
		Name        string  `json:"name"`
		Code        *string `json:"code"`
		Description string  `json:"description"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	l, err := h.people.CreateLocation(r.Context(), caller(w, r), application.NewLocationInput{Kind: body.Kind, ParentID: body.ParentID, Name: body.Name, Code: body.Code, Description: body.Description})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toLocation(l))
}

func (h *peopleHandler) updateLocation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int                   `json:"expectedVersion"`
		Name            *string               `json:"name"`
		Code            application.OptString `json:"code"`
		Description     *string               `json:"description"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	l, err := h.people.UpdateLocation(r.Context(), caller(w, r), r.PathValue("id"), application.LocationChange{ExpectedVersion: body.ExpectedVersion, Name: body.Name, Code: body.Code, Description: body.Description})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toLocation(l))
}

func (h *peopleHandler) moveLocation(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !peopleDecode(w, r, &raw) {
		return
	}
	version, parent, valid := parseLinkBody(raw, "parentId")
	if !valid {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "expectedVersion and parentId (a UUID or null) are required.")
		return
	}
	l, err := h.people.MoveLocation(r.Context(), caller(w, r), r.PathValue("id"), version, parent)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toLocation(l))
}

func (h *peopleHandler) locationActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedVersion int  `json:"expectedVersion"`
			ConfirmImpact   bool `json:"confirmImpact"`
		}
		if !peopleDecode(w, r, &body) {
			return
		}
		l, err := h.people.SetLocationActive(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, active, body.ConfirmImpact)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		ok(w, toLocation(l))
	}
}

// ---- departments ----

func (h *peopleHandler) createDepartment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		Code     *string `json:"code"`
		ParentID *string `json:"parentId"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	d, err := h.people.CreateDepartment(r.Context(), caller(w, r), application.NewDepartmentInput{Name: body.Name, Code: body.Code, ParentID: body.ParentID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDepartment(d))
}

func (h *peopleHandler) updateDepartment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int                   `json:"expectedVersion"`
		Name            *string               `json:"name"`
		Code            application.OptString `json:"code"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	d, err := h.people.UpdateDepartment(r.Context(), caller(w, r), r.PathValue("id"), application.DepartmentChange{ExpectedVersion: body.ExpectedVersion, Name: body.Name, Code: body.Code})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toDepartment(d))
}

func (h *peopleHandler) moveDepartment(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !peopleDecode(w, r, &raw) {
		return
	}
	version, parent, valid := parseLinkBody(raw, "parentId")
	if !valid {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "expectedVersion and parentId (a UUID or null) are required.")
		return
	}
	d, err := h.people.MoveDepartment(r.Context(), caller(w, r), r.PathValue("id"), version, parent)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toDepartment(d))
}

func (h *peopleHandler) departmentActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedVersion int  `json:"expectedVersion"`
			ConfirmImpact   bool `json:"confirmImpact"`
		}
		if !peopleDecode(w, r, &body) {
			return
		}
		d, err := h.people.SetDepartmentActive(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, active, body.ConfirmImpact)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		ok(w, toDepartment(d))
	}
}
