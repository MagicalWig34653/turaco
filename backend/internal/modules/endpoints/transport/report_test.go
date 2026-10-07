package transport

import (
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

func TestCSVCellNeutralizesFormulas(t *testing.T) {
	tests := map[string]string{
		"plain":                  "plain",
		"":                       "",
		"=HYPERLINK(\"x\")":      "'=HYPERLINK(\"x\")",
		"+1+1":                   "'+1+1",
		"-2":                     "'-2",
		"@SUM(A1)":               "'@SUM(A1)",
		"\t=1":                   "'\t=1",
		"  =cmd|' /C calc'!A0":   "'  =cmd|' /C calc'!A0",
		"a=b":                    "a=b",
		"0x87D1041C":             "0x87D1041C",
		"line\nbreak":            "line break",
		"\r=1":                   "' =1",
		"PC-01 (=not a formula)": "PC-01 (=not a formula)",
	}
	for in, want := range tests {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func csvTestServer(t *testing.T, export reportExporter) *httptest.Server {
	t.Helper()
	h := &handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.streamReportCSV(w, r, export) }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestCSVStoreErrorAfterTheResponseBeganAbortsTheConnection(t *testing.T) {
	srv := csvTestServer(t, func(begin func(application.Deployment), emit func(application.ReportRow) error) (bool, error) {
		begin(application.Deployment{Reference: "DEP-1"})
		_ = emit(application.ReportRow{ID: "t1", DeviceID: "d1", State: "failed"})
		return false, errors.New("store went away")
	})
	resp, err := http.Get(srv.URL)
	if err == nil {
		defer resp.Body.Close()
		if _, err = io.ReadAll(resp.Body); err == nil {
			t.Fatal("a failing export ended as a complete-looking file")
		}
	}
}

func TestCSVCompleteExportAndEarlyErrors(t *testing.T) {
	srv := csvTestServer(t, func(begin func(application.Deployment), emit func(application.ReportRow) error) (bool, error) {
		begin(application.Deployment{Reference: "DEP-1"})
		return false, emit(application.ReportRow{ID: "t1", DeviceID: "d1", State: "failed"})
	})
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "d1") {
		t.Fatalf("complete export: %d %v %q", resp.StatusCode, err, body)
	}
	busy := csvTestServer(t, func(func(application.Deployment), func(application.ReportRow) error) (bool, error) {
		return false, application.ErrExportBusy
	})
	resp, err = http.Get(busy.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(b), "endpoints.export_busy") {
		t.Fatalf("busy: %d %s", resp.StatusCode, b)
	}
}
