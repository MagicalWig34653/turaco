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

func TestCSVExportNeutralizesFormulaCells(t *testing.T) {
	srv := csvTestServer(t, func(begin func(application.Deployment), emit func(application.ReportRow) error) (bool, error) {
		begin(application.Deployment{Reference: "DEP-1"})
		reason := "+cmd|' /C calc'!A0"
		_ = emit(application.ReportRow{RingName: "=HYPERLINK(\"http://evil\")", DeviceID: "d1", DeviceName: "@SUM(A1)", State: "failed", StateReason: &reason, ErrorCode: "0x87D1041C"})
		return false, nil
	})
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.HasPrefix(body, "\ufeff") {
		t.Error("export lacks the UTF-8 byte order mark")
	}
	for _, want := range []string{`"'=HYPERLINK(""http://evil"")"`, `'@SUM(A1)`, `'+cmd|' /C calc'!A0`, `0x87D1041C`} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{",=HYPERLINK", ",@SUM", ",+cmd", `,"=HYPERLINK`} {
		if strings.Contains(body, bad) {
			t.Errorf("export contains un-neutralized cell %q:\n%s", bad, body)
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
