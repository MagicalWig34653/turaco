package application

import "testing"

func TestCleanHTTPSURLAcceptsOnlyPublicDNSNames(t *testing.T) {
	ok := []string{"https://downloads.example.com/app.msi", "https://dl.vendor.io:8443/a/b.exe?x=1", "https://cdn.example.co.uk/x"}
	bad := []string{
		"http://downloads.example.com/a", "https://user:pw@example.com/a", "https://user@example.com/a",
		"https://127.0.0.1/a", "https://10.0.0.5/a", "https://[::1]/a", "https://[fe80::1]/a", "https://169.254.169.254/latest",
		"https://2130706433/a", "https://0x7f.0x1/a", "https://localhost/a", "https://app.localhost/a", "https://nas.local/a",
		"https://build.internal/a", "https://files.corp/a", "https://router.home.arpa/a", "https://intranet/a",
		"https://xn--bcher-kva.example/a", "https://bücher.example/a", "https://exa mple.com/a", "https://example.com/ä",
	}
	for _, u := range ok {
		if _, valid := cleanHTTPSURL(u, 2000); !valid {
			t.Errorf("rejected %s", u)
		}
	}
	for _, u := range bad {
		if _, valid := cleanHTTPSURL(u, 2000); valid {
			t.Errorf("accepted %s", u)
		}
	}
}
