package public

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

type installationReader struct {
	Reader
	rows  []application.InstallationRow
	after string
}

func (r *installationReader) InstallationsByProducts(_ context.Context, _ []string, _ bool, after string, _ int) ([]application.InstallationRow, error) {
	r.after = after
	return r.rows, nil
}

func TestInstallationCursorIncludesProduct(t *testing.T) {
	const firstProduct = "00000000-0000-0000-0000-000000000001"
	const secondProduct = "00000000-0000-0000-0000-000000000002"
	const installation = "00000000-0000-0000-0000-000000000003"
	r := &installationReader{rows: []application.InstallationRow{
		{ID: installation, SoftwareProductID: firstProduct, ObservedAt: time.Now()},
		{ID: installation, SoftwareProductID: secondProduct, ObservedAt: time.Now()},
	}}
	inv := NewInventory(r)
	page, err := inv.InstallationsByProducts(context.Background(), []string{firstProduct, secondProduct}, false, Scope{}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != firstProduct+":"+installation {
		t.Fatalf("cursor = %q", page.NextCursor)
	}
	_, err = inv.InstallationsByProducts(context.Background(), []string{firstProduct}, false, Scope{}, page.NextCursor, 1)
	if err != nil || r.after != page.NextCursor {
		t.Fatalf("cursor was not forwarded: after=%q err=%v", r.after, err)
	}
	_, err = inv.InstallationsByProducts(context.Background(), []string{firstProduct}, false, Scope{}, installation, 1)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("accepted unpaired cursor: %v", err)
	}
}
