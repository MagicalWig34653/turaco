package transport

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestInvalidQueryBytesAreRejected(t *testing.T) {
	for _, q := range []string{"%00", "a%00b", "%FF", "a%FFb"} {
		rec, _ := serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users?q="+q)
		if rec.Code != 400 {
			t.Errorf("q=%s status = %d, want 400", q, rec.Code)
		}
	}
}

// The user DTO must never gain sensitive fields (employee number, DN, username).
func TestUserDTOFieldSet(t *testing.T) {
	raw, err := json.Marshal(userDTO{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	// accountKind, source, statusSource and version are not HR data; employee number, expiry and credentials are
	// only in the view_details responses.
	want := []string{"accountKind", "departmentId", "displayName", "familyName", "givenName", "id", "managerUserId", "primaryEmail", "primaryLocationId",
		"source", "status", "statusSource", "updatedAt", "version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user DTO fields = %v, want %v", got, want)
	}
}
