package public

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

type fakeReader struct {
	application.Reader
	user application.User
	err  error
}

func (f fakeReader) GetUser(context.Context, string) (application.User, error) { return f.user, f.err }

func TestIsActive(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		r       fakeReader
		want    bool
		wantErr bool
	}{
		{"active", fakeReader{user: application.User{Status: "active"}}, true, false},
		{"inactive", fakeReader{user: application.User{Status: "inactive"}}, false, false},
		{"departed", fakeReader{user: application.User{Status: "departed"}}, false, false},
		{"not found", fakeReader{err: fmt.Errorf("wrap: %w", application.ErrNotFound)}, false, false},
		{"other error", fakeReader{err: boom}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewUserAccess(tt.r).IsActive(context.Background(), "u")
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestSessionNames(t *testing.T) {
	given := "Lena"
	d, g, err := NewUserAccess(fakeReader{user: application.User{DisplayName: "Lena Hoffmann", GivenName: &given}}).SessionNames(context.Background(), "u")
	if err != nil || d != "Lena Hoffmann" || g != "Lena" {
		t.Fatalf("got %q %q %v", d, g, err)
	}
	d, g, err = NewUserAccess(fakeReader{user: application.User{DisplayName: "Development Admin"}}).SessionNames(context.Background(), "u")
	if err != nil || d != "Development Admin" || g != "" {
		t.Fatalf("got %q %q %v", d, g, err)
	}
	d, _, err = NewUserAccess(fakeReader{err: application.ErrNotFound}).SessionNames(context.Background(), "u")
	if err != nil || d != "" {
		t.Fatalf("not found: %q %v", d, err)
	}
	if _, _, err = NewUserAccess(fakeReader{err: errors.New("boom")}).SessionNames(context.Background(), "u"); err == nil {
		t.Fatal("want error")
	}
}
