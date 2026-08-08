package connectsvc

import "testing"

func TestOIDCRedirectsStayOnTrustedOrigins(t *testing.T) {
	if got := safeReturnTo("//evil.example/path"); got != "/sessions" {
		t.Fatalf("protocol-relative return accepted: %q", got)
	}
	if got := safeReturnTo("/settings?section=agents"); got != "/settings?section=agents" {
		t.Fatalf("local return rejected: %q", got)
	}
	if !validDesktopCallback("http://127.0.0.1:8787/desktop/oidc/callback?environment_id=one") {
		t.Fatal("Brigade.app callback rejected")
	}
	if validDesktopCallback("http://127.0.0.1:9999/desktop/oidc/callback") {
		t.Fatal("foreign loopback port accepted")
	}
}
