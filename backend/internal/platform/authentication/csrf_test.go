package authentication

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireSameOrigin(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"cross-site origin", "POST", map[string]string{"Origin": "https://evil.example"}, 403},
		{"cross-site fetch metadata", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site fetch metadata", "POST", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"missing both", "POST", nil, 403},
		{"null origin", "POST", map[string]string{"Origin": "null"}, 403},
		{"same origin header", "POST", map[string]string{"Origin": "https://app.example"}, 200},
		{"origin scheme insensitive", "POST", map[string]string{"Origin": "http://app.example"}, 200},
		{"origin other port", "POST", map[string]string{"Origin": "https://app.example:8443"}, 403},
		{"sec-fetch same-origin", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"sec-fetch none", "DELETE", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"same-origin fetch but foreign origin", "POST", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://evil.example"}, 403},
		{"GET unaffected", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"GET without headers", "GET", nil, 200},
		{"HEAD unaffected", "HEAD", nil, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireSameOrigin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest(tt.method, "http://app.example/x", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == 403 && !strings.Contains(rec.Body.String(), "platform.csrf_rejected") {
				t.Fatalf("body = %s", rec.Body)
			}
		})
	}
}
