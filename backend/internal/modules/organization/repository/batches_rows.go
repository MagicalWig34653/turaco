package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// Row planners and executors of imports and bulk operations. Every write goes through the transactional operations
// of the single-record API (createLocalUserTx, updateProfileTx, setManagerTx, changeStatusTx and so on), so field
// ownership, dominance, last administrator, hierarchy and uniqueness rules are the same.

func strRef(s string) *string { return &s }

func (r *Repository) PreviewImport(ctx context.Context, c application.Caller, in application.ParsedImport) (application.BatchPreview, error) {
	if err := r.requireGuards(); err != nil {
		return application.BatchPreview{}, err
	}
	if !c.CanApply(in.Kind) {
		return application.BatchPreview{}, application.ErrForbidden
	}
	id, err := r.newBatchID(ctx)
	if err != nil {
		return application.BatchPreview{}, err
	}
	bc := c
	bc.CorrelationID = batchCorrelation(id)
	rows := make([]rowSpec, len(in.Rows))
	for i, p := range in.Rows {
		rows[i] = rowSpec{no: p.No, key: p.Key, data: p.Data, preErrors: p.Errors, warnings: p.Warnings}
	}
	var exec func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error)
	switch in.Kind {
	case application.ImportUsers:
		exec = func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error) {
			return r.previewUserRow(ctx, tx, bc, in.MatchKey, in.Mode, s)
		}
	case application.ImportLocations:
		exec = func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error) {
			return r.previewLocationRow(ctx, tx, bc, in.Mode, s)
		}
	case application.ImportDepartments:
		exec = func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error) {
			return r.previewDepartmentRow(ctx, tx, bc, in.Mode, s)
		}
	default:
		return application.BatchPreview{}, fmt.Errorf("preview import: unknown kind %q", in.Kind)
	}
	res, err := r.runDry(ctx, rows, exec)
	if err != nil {
		return application.BatchPreview{}, finishBatch(err, "preview import")
	}
	hash := previewHash(in.Kind, in.MatchKey, in.Mode, "", nil, c.Actor.UserID, rows, res)
	return r.storeBatch(ctx, c, batchInsert{id: id, kind: in.Kind, matchKey: in.MatchKey, mode: in.Mode, params: map[string]any{}, fileHash: in.FileHash,
		hash: hash, unknown: in.UnknownColumns, rows: rows, res: res})
}

// runDry executes the rows in a transaction that is always rolled back.
func (r *Repository) runDry(ctx context.Context, rows []rowSpec, exec func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error)) ([]outcome, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin dry run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return dryRun(ctx, tx, rows, exec)
}

// ---- users ----

type userPlan struct {
	create                  bool
	cur                     application.User
	data                    map[string]string
	deptID, locID, mgrID    *string
	setDept, setLoc, setMgr bool
	profile                 application.ProfileChange
	diff                    map[string]application.DiffValue
}

// findUser looks a User up by the match key; more than one match is an ambiguous key.
func findUser(ctx context.Context, tx pgx.Tx, matchKey, key string) (application.User, bool, *application.RowIssue, error) {
	col := "lower(primary_email) = lower($1)"
	if matchKey == application.KeyEmployeeNumber {
		col = "employee_number = $1"
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM organization.users WHERE `+col+` LIMIT 2`, key)
	if err != nil {
		return application.User{}, false, nil, fmt.Errorf("find user: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return application.User{}, false, nil, fmt.Errorf("scan user id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return application.User{}, false, nil, fmt.Errorf("find user: %w", err)
	}
	switch len(ids) {
	case 0:
		return application.User{}, false, nil, nil
	case 1:
		u, err := reloadUser(ctx, tx, ids[0])
		if err != nil {
			return application.User{}, false, nil, fmt.Errorf("load user: %w", err)
		}
		return u, true, nil, nil
	}
	return application.User{}, false, &application.RowIssue{Field: matchKey, Code: "ambiguous_key"}, nil
}

func refByCode(ctx context.Context, tx pgx.Tx, table, code string) (*string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM organization.`+table+` WHERE lower(code) = lower($1) AND active`, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve %s code: %w", table, err)
	}
	return &id, nil
}

// refLabel is what the preview shows for a reference: its code, else its name (a User: the email, else the name).
func refLabel(ctx context.Context, tx pgx.Tx, table string, id *string) (*string, error) {
	if id == nil {
		return nil, nil
	}
	q := `SELECT coalesce(code, name) FROM organization.` + table + ` WHERE id = $1::uuid`
	if table == "users" {
		q = `SELECT coalesce(primary_email, display_name) FROM organization.users WHERE id = $1::uuid`
	}
	var label string
	err := tx.QueryRow(ctx, q, *id).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("label %s: %w", table, err)
	}
	return &label, nil
}

func diffOf(from *string, to string) application.DiffValue {
	return application.DiffValue{From: from, To: &to}
}

// planUser compares the row with the current User (nil: create) and resolves the references.
func (r *Repository) planUser(ctx context.Context, tx pgx.Tx, data map[string]string, cur *application.User) (userPlan, *application.RowIssue, error) {
	p := userPlan{create: cur == nil, data: data, diff: map[string]application.DiffValue{}}
	if cur != nil {
		p.cur = *cur
	}
	resolve := func(field, table string) (*string, *application.RowIssue, error) {
		code := data[field]
		id, err := refByCode(ctx, tx, table, code)
		if err != nil {
			return nil, nil, err
		}
		if id == nil {
			return nil, &application.RowIssue{Field: field, Code: "reference_not_found"}, nil
		}
		return id, nil, nil
	}
	if data["department_code"] != "" {
		id, issue, err := resolve("department_code", "departments")
		if err != nil || issue != nil {
			return p, issue, err
		}
		p.deptID, p.setDept = id, true
	}
	if data["location_code"] != "" {
		id, issue, err := resolve("location_code", "locations")
		if err != nil || issue != nil {
			return p, issue, err
		}
		p.locID, p.setLoc = id, true
	}
	if data["manager_email"] != "" {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM organization.users WHERE lower(primary_email) = lower($1)`, data["manager_email"]).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return p, &application.RowIssue{Field: "manager_email", Code: "reference_not_found"}, nil
		}
		if err != nil {
			return p, nil, fmt.Errorf("resolve manager: %w", err)
		}
		p.mgrID, p.setMgr = &id, true
	}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	type textField struct{ col, name string }
	if p.create {
		for _, f := range []string{"display_name", "given_name", "family_name", "primary_email", "employee_number", "department_code", "location_code", "manager_email"} {
			if data[f] != "" {
				p.diff[f] = diffOf(nil, data[f])
			}
		}
		return p, nil, nil
	}
	set := func(name string, has bool, v string, curVal string, assign func(application.OptString)) {
		if has && v != curVal {
			assign(application.OptString{Set: true, Value: &v})
			p.diff[name] = diffOf(ptrIfNotEmpty(curVal), v)
		}
	}
	set("display_name", data["display_name"] != "", data["display_name"], cur.DisplayName, func(o application.OptString) { p.profile.DisplayName = o })
	set("given_name", data["given_name"] != "", data["given_name"], str(cur.GivenName), func(o application.OptString) { p.profile.GivenName = o })
	set("family_name", data["family_name"] != "", data["family_name"], str(cur.FamilyName), func(o application.OptString) { p.profile.FamilyName = o })
	set("employee_number", data["employee_number"] != "", data["employee_number"], str(cur.EmployeeNumber), func(o application.OptString) { p.profile.EmployeeNumber = o })
	if v := data["primary_email"]; v != "" && !strings.EqualFold(v, str(cur.PrimaryEmail)) {
		p.profile.PrimaryEmail = application.OptString{Set: true, Value: &v}
		p.diff["primary_email"] = diffOf(ptrIfNotEmpty(str(cur.PrimaryEmail)), v)
	}
	refChange := func(name, table string, has bool, to, curID *string, field string) error {
		if !has || (curID != nil && strings.EqualFold(*curID, *to)) {
			return nil
		}
		from, err := refLabel(ctx, tx, table, curID)
		if err != nil {
			return err
		}
		p.diff[name] = diffOf(from, data[field])
		return nil
	}
	if err := refChange("department_code", "departments", p.setDept, p.deptID, cur.DepartmentID, "department_code"); err != nil {
		return p, nil, err
	}
	if err := refChange("location_code", "locations", p.setLoc, p.locID, cur.PrimaryLocationID, "location_code"); err != nil {
		return p, nil, err
	}
	if err := refChange("manager_email", "users", p.setMgr, p.mgrID, cur.ManagerUserID, "manager_email"); err != nil {
		return p, nil, err
	}
	return p, nil, nil
}

func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// execUser performs the writes of a planned row.
func (r *Repository) execUser(ctx context.Context, tx pgx.Tx, c application.Caller, p userPlan) error {
	if p.create {
		d := p.data
		in := application.NewUserInput{DisplayName: d["display_name"], DepartmentID: p.deptID, LocationID: p.locID, AccountKind: application.AccountKindEmployee,
			GivenName: ptrIfNotEmpty(d["given_name"]), FamilyName: ptrIfNotEmpty(d["family_name"]),
			PrimaryEmail: ptrIfNotEmpty(d["primary_email"]), EmployeeNumber: ptrIfNotEmpty(d["employee_number"])}
		u, err := r.createLocalUserTx(ctx, tx, c, in)
		if err != nil {
			return err
		}
		if p.setMgr {
			_, err = r.setManagerTx(ctx, tx, c, u.ID, u.Version, p.mgrID)
		}
		return err
	}
	version := p.cur.Version
	id := p.cur.ID
	if len(profileFields(p.profile)) > 0 {
		in := p.profile
		in.ExpectedVersion = version
		u, err := r.updateProfileTx(ctx, tx, c, id, in)
		if err != nil {
			return err
		}
		version = u.Version
	}
	if p.setDept && !equalPtr(p.cur.DepartmentID, p.deptID) {
		u, err := r.setDepartmentTx(ctx, tx, c, id, version, p.deptID)
		if err != nil {
			return err
		}
		version = u.Version
	}
	if p.setLoc && !equalPtr(p.cur.PrimaryLocationID, p.locID) {
		u, err := r.setPrimaryLocationTx(ctx, tx, c, id, version, p.locID)
		if err != nil {
			return err
		}
		version = u.Version
	}
	if p.setMgr && !equalPtr(p.cur.ManagerUserID, p.mgrID) {
		if _, err := r.setManagerTx(ctx, tx, c, id, version, p.mgrID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) setDepartmentTx(ctx context.Context, tx pgx.Tx, c application.Caller, id string, version int, target *string) (application.User, error) {
	return r.setUserLinkTx(ctx, tx, c, id, version, "department_id", "organization.user.department_set", target,
		func(ctx context.Context, tx pgx.Tx, _ application.User, t string) error {
			return requireActive(ctx, tx, "departments", t)
		},
		func(u application.User) *string { return u.DepartmentID })
}

func (r *Repository) setPrimaryLocationTx(ctx context.Context, tx pgx.Tx, c application.Caller, id string, version int, target *string) (application.User, error) {
	return r.setUserLinkTx(ctx, tx, c, id, version, "primary_location_id", "organization.user.location_set", target,
		func(ctx context.Context, tx pgx.Tx, _ application.User, t string) error {
			return requireActive(ctx, tx, "locations", t)
		},
		func(u application.User) *string { return u.PrimaryLocationID })
}

func (r *Repository) previewUserRow(ctx context.Context, tx pgx.Tx, c application.Caller, matchKey, mode string, s rowSpec) (outcome, error) {
	cur, exists, issue, err := findUser(ctx, tx, matchKey, s.key)
	if err != nil {
		return outcome{}, err
	}
	if issue != nil {
		return reject(*issue), nil
	}
	if exists && mode == application.ModeCreateOnly {
		return reject(application.RowIssue{Field: matchKey, Code: "already_exists"}), nil
	}
	if !exists && mode == application.ModeUpdateOnly {
		return reject(application.RowIssue{Field: matchKey, Code: "not_found"}), nil
	}
	if !exists && s.data["display_name"] == "" {
		return reject(application.RowIssue{Field: "display_name", Code: application.IssueDisplayNameRequired}), nil
	}
	var curPtr *application.User
	if exists {
		curPtr = &cur
	}
	plan, issue, err := r.planUser(ctx, tx, s.data, curPtr)
	if err != nil {
		return outcome{}, err
	}
	if issue != nil {
		return reject(*issue), nil
	}
	if exists && len(plan.diff) == 0 {
		return outcome{action: application.RowUnchanged, targetID: cur.ID, targetVersion: cur.Version}, nil
	}
	if err := r.execUser(ctx, tx, c, plan); err != nil {
		if is, ok := issueOf(err); ok {
			return reject(is), nil
		}
		return outcome{}, err
	}
	if !exists {
		return outcome{action: application.RowCreate, diff: plan.diff}, nil
	}
	return outcome{action: application.RowUpdate, diff: plan.diff, targetID: cur.ID, targetVersion: cur.Version}, nil
}

func (r *Repository) applyUserRow(ctx context.Context, tx pgx.Tx, c application.Caller, matchKey string, s rowSpec) error {
	var cur *application.User
	if s.action == application.RowUpdate {
		u, err := reloadUser(ctx, tx, s.targetID)
		if err != nil {
			return application.ErrNotFound
		}
		if u.Version != s.targetVersion {
			return application.ErrVersionConflict
		}
		cur = &u
	}
	plan, issue, err := r.planUser(ctx, tx, s.data, cur)
	if err != nil {
		return err
	}
	if issue != nil {
		return application.ErrImportStale
	}
	return r.execUser(ctx, tx, c, plan)
}

// ---- locations ----

func (r *Repository) previewLocationRow(ctx context.Context, tx pgx.Tx, c application.Caller, mode string, s rowSpec) (outcome, error) {
	cur, exists, err := findByCode(ctx, tx, "locations", s.key, func(id string) (any, error) { return reloadLocation(ctx, tx, id) })
	if err != nil {
		return outcome{}, err
	}
	if exists && mode == application.ModeCreateOnly {
		return reject(application.RowIssue{Field: "code", Code: "already_exists"}), nil
	}
	if !exists && mode == application.ModeUpdateOnly {
		return reject(application.RowIssue{Field: "code", Code: "not_found"}), nil
	}
	var curLoc *application.Location
	if exists {
		l := cur.(application.Location)
		curLoc = &l
	}
	plan, issue, err := r.planLocation(ctx, tx, s, curLoc)
	if err != nil {
		return outcome{}, err
	}
	if issue != nil {
		return reject(*issue), nil
	}
	if exists && len(plan.diff) == 0 {
		return outcome{action: application.RowUnchanged, targetID: curLoc.ID, targetVersion: curLoc.Version}, nil
	}
	if err := r.execLocation(ctx, tx, c, plan); err != nil {
		if is, ok := issueOf(err); ok {
			return reject(is), nil
		}
		return outcome{}, err
	}
	if !exists {
		return outcome{action: application.RowCreate, diff: plan.diff}, nil
	}
	return outcome{action: application.RowUpdate, diff: plan.diff, targetID: curLoc.ID, targetVersion: curLoc.Version}, nil
}

// findByCode finds the active Location or Department with the code and loads it through load.
func findByCode(ctx context.Context, tx pgx.Tx, table, code string, load func(id string) (any, error)) (any, bool, error) {
	id, err := refByCode(ctx, tx, table, code)
	if err != nil {
		return nil, false, err
	}
	if id == nil {
		return nil, false, nil
	}
	v, err := load(*id)
	if err != nil {
		return nil, false, fmt.Errorf("load %s: %w", table, err)
	}
	return v, true, nil
}

type treePlan struct {
	create   bool
	id       string
	version  int
	data     map[string]string
	kind     string
	parentID *string
	name     *string
	descr    *string
	diff     map[string]application.DiffValue
}

func (r *Repository) planLocation(ctx context.Context, tx pgx.Tx, s rowSpec, cur *application.Location) (treePlan, *application.RowIssue, error) {
	p := treePlan{create: cur == nil, data: s.data, diff: map[string]application.DiffValue{}}
	d := s.data
	if d["parent_code"] != "" {
		id, err := refByCode(ctx, tx, "locations", d["parent_code"])
		if err != nil {
			return p, nil, err
		}
		if id == nil {
			return p, &application.RowIssue{Field: "parent_code", Code: "reference_not_found"}, nil
		}
		p.parentID = id
	}
	if cur == nil {
		if d["name"] == "" {
			return p, &application.RowIssue{Field: "name", Code: "name_required"}, nil
		}
		p.kind = d["kind"]
		if p.kind == "" {
			p.kind = application.LocationSite
			if p.parentID != nil {
				p.kind = application.LocationArea
			}
		}
		if (p.kind == application.LocationSite) != (p.parentID == nil) {
			return p, &application.RowIssue{Field: "kind", Code: application.IssueInvalidValue}, nil
		}
		for _, f := range []string{"code", "name", "kind", "parent_code", "description"} {
			if d[f] != "" {
				p.diff[f] = diffOf(nil, d[f])
			}
		}
		return p, nil, nil
	}
	p.id, p.version = cur.ID, cur.Version
	if d["kind"] != "" && d["kind"] != cur.Kind {
		return p, &application.RowIssue{Field: "kind", Code: "kind_change_not_supported"}, nil
	}
	if d["parent_code"] != "" && !equalPtr(p.parentID, cur.ParentID) {
		return p, &application.RowIssue{Field: "parent_code", Code: "parent_change_not_supported"}, nil
	}
	if v := d["name"]; v != "" && v != cur.Name {
		p.name = &v
		p.diff["name"] = diffOf(strRef(cur.Name), v)
	}
	if v := d["description"]; v != "" && v != cur.Description {
		p.descr = &v
		p.diff["description"] = diffOf(ptrIfNotEmpty(cur.Description), v)
	}
	return p, nil, nil
}

func (r *Repository) execLocation(ctx context.Context, tx pgx.Tx, c application.Caller, p treePlan) error {
	if p.create {
		code := p.data["code"]
		_, err := r.createLocationTx(ctx, tx, c, application.NewLocationInput{Kind: p.kind, ParentID: p.parentID, Name: p.data["name"], Code: &code, Description: p.data["description"]})
		return err
	}
	_, err := r.updateLocationTx(ctx, tx, c, p.id, application.LocationChange{ExpectedVersion: p.version, Name: p.name, Description: p.descr})
	return err
}

func (r *Repository) applyLocationRow(ctx context.Context, tx pgx.Tx, c application.Caller, s rowSpec) error {
	var cur *application.Location
	if s.action == application.RowUpdate {
		l, err := reloadLocation(ctx, tx, s.targetID)
		if err != nil {
			return application.ErrNotFound
		}
		if l.Version != s.targetVersion {
			return application.ErrVersionConflict
		}
		cur = &l
	}
	plan, issue, err := r.planLocation(ctx, tx, s, cur)
	if err != nil {
		return err
	}
	if issue != nil {
		return application.ErrImportStale
	}
	return r.execLocation(ctx, tx, c, plan)
}

// ---- departments ----

func (r *Repository) previewDepartmentRow(ctx context.Context, tx pgx.Tx, c application.Caller, mode string, s rowSpec) (outcome, error) {
	cur, exists, err := findByCode(ctx, tx, "departments", s.key, func(id string) (any, error) { return reloadDepartment(ctx, tx, id) })
	if err != nil {
		return outcome{}, err
	}
	if exists && mode == application.ModeCreateOnly {
		return reject(application.RowIssue{Field: "code", Code: "already_exists"}), nil
	}
	if !exists && mode == application.ModeUpdateOnly {
		return reject(application.RowIssue{Field: "code", Code: "not_found"}), nil
	}
	var curDep *application.Department
	if exists {
		d := cur.(application.Department)
		curDep = &d
	}
	plan, issue, err := r.planDepartment(ctx, tx, s, curDep)
	if err != nil {
		return outcome{}, err
	}
	if issue != nil {
		return reject(*issue), nil
	}
	if exists && len(plan.diff) == 0 {
		return outcome{action: application.RowUnchanged, targetID: curDep.ID, targetVersion: curDep.Version}, nil
	}
	if err := r.execDepartment(ctx, tx, c, plan); err != nil {
		if is, ok := issueOf(err); ok {
			return reject(is), nil
		}
		return outcome{}, err
	}
	if !exists {
		return outcome{action: application.RowCreate, diff: plan.diff}, nil
	}
	return outcome{action: application.RowUpdate, diff: plan.diff, targetID: curDep.ID, targetVersion: curDep.Version}, nil
}

func (r *Repository) planDepartment(ctx context.Context, tx pgx.Tx, s rowSpec, cur *application.Department) (treePlan, *application.RowIssue, error) {
	p := treePlan{create: cur == nil, data: s.data, diff: map[string]application.DiffValue{}}
	d := s.data
	if d["parent_code"] != "" {
		id, err := refByCode(ctx, tx, "departments", d["parent_code"])
		if err != nil {
			return p, nil, err
		}
		if id == nil {
			return p, &application.RowIssue{Field: "parent_code", Code: "reference_not_found"}, nil
		}
		p.parentID = id
	}
	if cur == nil {
		if d["name"] == "" {
			return p, &application.RowIssue{Field: "name", Code: "name_required"}, nil
		}
		for _, f := range []string{"code", "name", "parent_code"} {
			if d[f] != "" {
				p.diff[f] = diffOf(nil, d[f])
			}
		}
		return p, nil, nil
	}
	p.id, p.version = cur.ID, cur.Version
	if d["parent_code"] != "" && !equalPtr(p.parentID, cur.ParentID) {
		return p, &application.RowIssue{Field: "parent_code", Code: "parent_change_not_supported"}, nil
	}
	if v := d["name"]; v != "" && v != cur.Name {
		p.name = &v
		p.diff["name"] = diffOf(strRef(cur.Name), v)
	}
	return p, nil, nil
}

func (r *Repository) execDepartment(ctx context.Context, tx pgx.Tx, c application.Caller, p treePlan) error {
	if p.create {
		code := p.data["code"]
		_, err := r.createDepartmentTx(ctx, tx, c, application.NewDepartmentInput{Name: p.data["name"], Code: &code, ParentID: p.parentID})
		return err
	}
	_, err := r.updateDepartmentTx(ctx, tx, c, p.id, application.DepartmentChange{ExpectedVersion: p.version, Name: p.name})
	return err
}

func (r *Repository) applyDepartmentRow(ctx context.Context, tx pgx.Tx, c application.Caller, s rowSpec) error {
	var cur *application.Department
	if s.action == application.RowUpdate {
		d, err := reloadDepartment(ctx, tx, s.targetID)
		if err != nil {
			return application.ErrNotFound
		}
		if d.Version != s.targetVersion {
			return application.ErrVersionConflict
		}
		cur = &d
	}
	plan, issue, err := r.planDepartment(ctx, tx, s, cur)
	if err != nil {
		return err
	}
	if issue != nil {
		return application.ErrImportStale
	}
	return r.execDepartment(ctx, tx, c, plan)
}

// ---- bulk operations on Users ----

type bulkParams struct {
	DepartmentID  *string `json:"departmentId"`
	LocationID    *string `json:"locationId"`
	ManagerUserID *string `json:"managerUserId"`
	Reason        string  `json:"reason"`
}

func (r *Repository) bulkOp(ctx context.Context, tx pgx.Tx, c application.Caller, op string, p bulkParams, id string, version int) (application.User, error) {
	switch op {
	case application.BulkSetDepartment:
		return r.setDepartmentTx(ctx, tx, c, id, version, p.DepartmentID)
	case application.BulkSetPrimaryLocation:
		return r.setPrimaryLocationTx(ctx, tx, c, id, version, p.LocationID)
	case application.BulkSetManager:
		return r.setManagerTx(ctx, tx, c, id, version, p.ManagerUserID)
	case application.BulkDeactivate:
		return r.changeStatusTx(ctx, tx, c, id, version, application.OpDeactivate, p.Reason)
	}
	return application.User{}, fmt.Errorf("bulk: unknown operation %q", op)
}

func (r *Repository) PreviewBulk(ctx context.Context, c application.Caller, in application.BulkInput, ids []string) (application.BatchPreview, error) {
	if err := r.requireGuards(); err != nil {
		return application.BatchPreview{}, err
	}
	if !c.CanApply(application.BatchBulkUsers) {
		return application.BatchPreview{}, application.ErrForbidden
	}
	id, err := r.newBatchID(ctx)
	if err != nil {
		return application.BatchPreview{}, err
	}
	bc := c
	bc.CorrelationID = batchCorrelation(id)
	params := bulkParams{DepartmentID: in.DepartmentID, LocationID: in.LocationID, ManagerUserID: in.ManagerUserID, Reason: in.Reason}
	rows := make([]rowSpec, len(ids))
	for i, uid := range ids {
		rows[i] = rowSpec{no: i + 1, key: uid}
	}
	res, err := r.runDry(ctx, rows, func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error) {
		return r.previewBulkRow(ctx, tx, bc, in.Operation, params, s)
	})
	if err != nil {
		return application.BatchPreview{}, finishBatch(err, "preview bulk operation")
	}
	var pm map[string]any
	_ = json.Unmarshal(marshalJSON(params), &pm)
	hash := previewHash(application.BatchBulkUsers, "", "", in.Operation, pm, c.Actor.UserID, rows, res)
	return r.storeBatch(ctx, c, batchInsert{id: id, kind: application.BatchBulkUsers, operation: in.Operation, params: pm, hash: hash, rows: rows, res: res})
}

func (r *Repository) previewBulkRow(ctx context.Context, tx pgx.Tx, c application.Caller, op string, p bulkParams, s rowSpec) (outcome, error) {
	cur, err := reloadUser(ctx, tx, s.key)
	if errors.Is(err, pgx.ErrNoRows) {
		return reject(application.RowIssue{Code: "not_found"}), nil
	}
	if err != nil {
		return outcome{}, fmt.Errorf("load user: %w", err)
	}
	label := cur.DisplayName
	out, err := r.bulkOp(ctx, tx, c, op, p, s.key, cur.Version)
	if err != nil {
		if is, ok := issueOf(err); ok {
			o := reject(is)
			o.label = label
			return o, nil
		}
		return outcome{}, err
	}
	o := outcome{action: application.RowUnchanged, targetID: cur.ID, targetVersion: cur.Version, label: label}
	if out.Version == cur.Version {
		return o, nil
	}
	o.action = application.RowUpdate
	o.diff = map[string]application.DiffValue{}
	switch op {
	case application.BulkSetDepartment:
		from, e1 := refLabel(ctx, tx, "departments", cur.DepartmentID)
		to, e2 := refLabel(ctx, tx, "departments", out.DepartmentID)
		if err := errors.Join(e1, e2); err != nil {
			return outcome{}, err
		}
		o.diff["department"] = application.DiffValue{From: from, To: to}
	case application.BulkSetPrimaryLocation:
		from, e1 := refLabel(ctx, tx, "locations", cur.PrimaryLocationID)
		to, e2 := refLabel(ctx, tx, "locations", out.PrimaryLocationID)
		if err := errors.Join(e1, e2); err != nil {
			return outcome{}, err
		}
		o.diff["location"] = application.DiffValue{From: from, To: to}
	case application.BulkSetManager:
		from, e1 := refLabel(ctx, tx, "users", cur.ManagerUserID)
		to, e2 := refLabel(ctx, tx, "users", out.ManagerUserID)
		if err := errors.Join(e1, e2); err != nil {
			return outcome{}, err
		}
		o.diff["manager"] = application.DiffValue{From: from, To: to}
	case application.BulkDeactivate:
		o.diff["status"] = application.DiffValue{From: strRef(cur.Status), To: strRef(out.Status)}
	}
	return o, nil
}

func (r *Repository) applyBulkRow(ctx context.Context, tx pgx.Tx, c application.Caller, op string, params map[string]any, s rowSpec) error {
	var p bulkParams
	_ = json.Unmarshal(marshalJSON(params), &p)
	cur, err := reloadUser(ctx, tx, s.targetID)
	if err != nil {
		return application.ErrNotFound
	}
	if cur.Version != s.targetVersion {
		return application.ErrVersionConflict
	}
	_, err = r.bulkOp(ctx, tx, c, op, p, s.targetID, s.targetVersion)
	return err
}
