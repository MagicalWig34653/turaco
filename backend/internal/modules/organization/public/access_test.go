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
