package transport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

func TestMajorDTOHidesOwnerFromEmployees(t *testing.T) {
	owner, due := "00000000-0000-7000-8000-0000000000a1", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	m := application.MajorIncident{ID: "i1", Title: "Mail", IsExercise: true, OwnerUserID: &owner, NextUpdateDue: &due}
	emp, _ := json.Marshal(toMajor(m, false))
	if strings.Contains(string(emp), "ownerUserId") || strings.Contains(string(emp), owner) {
		t.Errorf("employee DTO leaks the owner: %s", emp)
	}
	pub, _ := json.Marshal(toMajorPublic(m))
	if strings.Contains(string(pub), owner) {
		t.Errorf("list DTO leaks the owner: %s", pub)
	}
	for _, want := range []string{`"isExercise":true`, `"nextUpdateDue":"2026-10-10T12:00:00Z"`} {
		if !strings.Contains(string(emp), want) {
			t.Errorf("DTO lacks %s: %s", want, emp)
		}
	}
	staff, _ := json.Marshal(toMajor(m, true))
	if !strings.Contains(string(staff), owner) {
		t.Errorf("staff DTO lacks the owner: %s", staff)
	}
}
