package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseImportUsers(t *testing.T) {
	csv := "\ufeffDisplay Name;primary_email;employee-number;department_code;extra\n" +
		"Anna Beispiel;anna@example.test;E1;IT;x\n" +
		"=cmd|' /C calc'!A0;bad-address;E2;;x\n" +
		"-Meier;meier@example.test;E3;;x\n" +
		"Dup One;dup@example.test;E4;;\n" +
		"Dup Two;DUP@example.test;E5;;\n" +
		";;;;\n" +
		"Short;short@example.test\n"
	p, err := ParseImport(ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 6 {
		t.Fatalf("rows = %d, want 6 (blank row skipped)", len(p.Rows))
	}
	if strings.Join(p.UnknownColumns, ",") != "extra" {
		t.Errorf("unknown columns = %v", p.UnknownColumns)
	}
	if len(p.FileHash) != 64 {
		t.Errorf("file hash = %q", p.FileHash)
	}
	byNo := map[int]ParsedRow{}
	for _, r := range p.Rows {
		byNo[r.No] = r
	}
	if r := byNo[1]; len(r.Errors) != 0 || r.Data["employee_number"] != "E1" || r.Data["department_code"] != "IT" {
		t.Errorf("row 1 = %+v", r)
	}
	// A formula prefix is a warning on the display name, the invalid address a row error; neither writes a cell back.
	if r := byNo[2]; !hasIssue(r.Warnings, "display_name", IssueFormulaPrefix) || !hasIssue(r.Errors, "primary_email", IssueInvalidValue) {
		t.Errorf("row 2 = %+v", r)
	}
	if r := byNo[3]; len(r.Errors) != 0 || !hasIssue(r.Warnings, "display_name", IssueFormulaPrefix) {
		t.Errorf("row 3 keeps names with a leading dash and warns: %+v", r)
	}
	for _, n := range []int{4, 5} {
		if !hasIssue(byNo[n].Errors, "primary_email", IssueDuplicateKey) {
			t.Errorf("row %d: duplicate keys (case-insensitive) must be rejected: %+v", n, byNo[n])
		}
	}
	if !hasIssue(byNo[6].Errors, "", IssueColumnCount) {
		t.Errorf("row 6 has too few cells: %+v", byNo[6])
	}
}

func TestParseImportFileLevelErrors(t *testing.T) {
	big := make([]byte, MaxImportBytes+1)
	rows := "primary_email\n" + strings.Repeat("a@example.test\n", 1)
	var many strings.Builder
	many.WriteString("code,name\n")
	for i := 0; i <= MaxImportRows; i++ {
		many.WriteString("C")
		many.WriteString(strings.Repeat("x", 1))
		many.WriteString(strings.Repeat("1", i%7))
		many.WriteString(",n\n")
	}
	tests := []struct {
		name, kind, key, mode string
		data                  []byte
		want                  error
		invalid               bool
	}{
		{"too large", ImportUsers, KeyPrimaryEmail, ModeUpsert, big, ErrImportTooLarge, false},
		{"too many rows", ImportDepartments, "", ModeUpsert, []byte(many.String()), ErrImportTooManyRows, false},
		{"empty", ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte("  \n"), nil, true},
		{"invalid utf8", ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte("primary_email\n\xff\n"), nil, true},
		{"missing key column", ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte("display_name\nA\n"), nil, true},
		{"duplicate column", ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte("primary_email,primary_email\na,b\n"), nil, true},
		{"no data rows", ImportUsers, KeyPrimaryEmail, ModeUpsert, []byte("primary_email\n"), nil, true},
		{"bad mode", ImportUsers, KeyPrimaryEmail, "replace", []byte(rows), nil, true},
		{"bad kind", "teams", KeyCode, ModeUpsert, []byte(rows), nil, true},
		{"users need a user match key", ImportUsers, KeyCode, ModeUpsert, []byte(rows), nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseImport(tc.kind, tc.key, tc.mode, tc.data)
			var inv *InvalidInputError
			switch {
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("err = %v, want %v", err, tc.want)
			case tc.invalid && !errors.As(err, &inv):
				t.Fatalf("err = %v, want InvalidInputError", err)
			case tc.want == nil && !tc.invalid && err != nil:
				t.Fatal(err)
			}
		})
	}
}

func TestParseImportDelimiterAndQuotes(t *testing.T) {
	p, err := ParseImport(ImportLocations, "", ModeCreateOnly, []byte("code,name,kind,description\nHQ,\"Head, Quarters\",SITE,\"line one\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Rows[0].Data; got["name"] != "Head, Quarters" || got["kind"] != "site" || p.Rows[0].Key != "HQ" {
		t.Errorf("row = %+v", p.Rows[0])
	}
	// Control characters in a cell reject the row (the NUL byte cannot be hidden in a name).
	p, err = ParseImport(ImportDepartments, "", ModeUpsert, []byte("code,name\nD1,Fin\x07ance\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(p.Rows[0].Errors, "name", IssueControlCharacters) {
		t.Errorf("control characters must be rejected: %+v", p.Rows[0])
	}
}

func TestValidBulkInput(t *testing.T) {
	id := "0198f3c2-0000-7000-8000-000000000001"
	ids, err := ValidBulkInput(BulkInput{Operation: BulkSetDepartment, UserIDs: []string{id, strings.ToUpper(id)}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids = %v, err = %v (duplicates collapse, case folds)", ids, err)
	}
	bad := []BulkInput{
		{Operation: "assign_role", UserIDs: []string{id}},
		{Operation: BulkDeactivate, UserIDs: []string{id}, Reason: "because"},
		{Operation: BulkSetDepartment},
		{Operation: BulkSetDepartment, UserIDs: []string{"not-a-uuid"}},
	}
	for i, in := range bad {
		if _, err := ValidBulkInput(in); err == nil {
			t.Errorf("case %d must be refused", i)
		}
	}
	many := make([]string, MaxBulkUsers+1)
	for i := range many {
		many[i] = id
	}
	if _, err := ValidBulkInput(BulkInput{Operation: BulkSetDepartment, UserIDs: many}); err == nil {
		t.Error("more than 500 ids in the request must be refused")
	}
}

func TestCallerPermissionsForBatches(t *testing.T) {
	has := func(perms ...string) Caller {
		set := map[string]bool{}
		for _, p := range perms {
			set[p] = true
		}
		return Caller{Has: func(p string) bool { return set[p] }}
	}
	if (Caller{}).CanApply(ImportUsers) || (Caller{}).CanView(BatchBulkUsers) {
		t.Error("a caller without Has fails closed")
	}
	if has(PermUsersManage).CanApply(ImportUsers) {
		t.Error("a CSV import needs organization.import in addition to the manage permission")
	}
	if !has(PermUsersManage, PermImport).CanApply(ImportUsers) || has(PermImport).CanApply(ImportUsers) {
		t.Error("import needs both permissions")
	}
	if !has(PermUsersManage).CanApply(BatchBulkUsers) {
		t.Error("bulk operations need the manage permission only")
	}
	if has(PermUsersManage, PermImport).CanApply(ImportLocations) {
		t.Error("a Locations import needs organization.locations.manage")
	}
}

type fakeBatchStore struct {
	BatchStore
	called string
}

func (f *fakeBatchStore) LinkDirectoryIdentity(context.Context, Caller, string, int, string, string) (User, error) {
	f.called = "link"
	return User{}, nil
}
func (f *fakeBatchStore) ExtendAccess(_ context.Context, _ Caller, _ string, _ int, _ time.Time, _ string) (User, error) {
	f.called = "extend"
	return User{}, nil
}

func TestLinkAndExtendValidation(t *testing.T) {
	store := &fakeBatchStore{}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	svc := NewImports(store).WithClock(func() time.Time { return now })
	base := Caller{CorrelationID: "c"}
	base.Actor.UserID = "0198f3c2-0000-7000-8000-0000000000aa"
	run := "0198f3c2-0000-7000-8000-0000000000bb"

	if _, err := svc.LinkDirectoryIdentity(context.Background(), base, "id", 1, run, "ext"); !errors.Is(err, ErrAdminRequired) {
		t.Errorf("link without platform admin = %v", err)
	}
	admin := base
	admin.PlatformAdmin = true
	if _, err := svc.LinkDirectoryIdentity(context.Background(), admin, "id", 0, run, "ext"); err == nil {
		t.Error("link needs expectedVersion")
	}
	if _, err := svc.LinkDirectoryIdentity(context.Background(), admin, "id", 1, "nope", "ext"); err == nil {
		t.Error("link needs a run id")
	}
	if _, err := svc.LinkDirectoryIdentity(context.Background(), admin, "id", 1, run, "ext"); err != nil || store.called != "link" {
		t.Errorf("valid link = %v (%s)", err, store.called)
	}

	if _, err := svc.ExtendAccess(context.Background(), base, "id", 1, now.AddDate(0, 0, 10), "contract_renewed"); !errors.Is(err, ErrForbidden) {
		t.Errorf("extend without external_parties.manage = %v", err)
	}
	ext := base
	ext.ExternalPartiesManage = true
	for name, tc := range map[string]struct {
		until  time.Time
		reason string
	}{
		"past":           {now.Add(-time.Hour), "contract_renewed"},
		"beyond 365":     {now.AddDate(0, 0, 366), "contract_renewed"},
		"unknown":        {now.AddDate(0, 0, 10), "because"},
		"empty reason":   {now.AddDate(0, 0, 10), ""},
		"zero timepoint": {time.Time{}, "contract_renewed"},
	} {
		if _, err := svc.ExtendAccess(context.Background(), ext, "id", 1, tc.until, tc.reason); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := svc.ExtendAccess(context.Background(), ext, "id", 1, now.AddDate(0, 0, 365), "project_extended"); err != nil || store.called != "extend" {
		t.Errorf("valid extend = %v (%s)", err, store.called)
	}
}

func hasIssue(issues []RowIssue, field, code string) bool {
	for _, i := range issues {
		if i.Field == field && i.Code == code {
			return true
		}
	}
	return false
}
