package auth

import (
	"net/http"
	"testing"
)

// TestTokenFromHeaderOrCookie проверяет приоритет источников access-токена:
// Bearer-заголовок (mobile) предпочтительнее httpOnly-cookie (web).
func TestTokenFromHeaderOrCookie(t *testing.T) {
	cookieOnly := http.Header{}
	withCookie := func(h http.Header, val string) http.Header {
		r := &http.Request{Header: http.Header{}}
		r.AddCookie(&http.Cookie{Name: AccessCookieName, Value: val})
		h.Set("Cookie", r.Header.Get("Cookie"))
		return h
	}

	tests := []struct {
		name   string
		header http.Header
		want   string
	}{
		{
			name:   "bearer header",
			header: http.Header{"Authorization": {"Bearer abc.def.ghi"}},
			want:   "abc.def.ghi",
		},
		{
			name:   "bearer trims spaces",
			header: http.Header{"Authorization": {"Bearer   token-with-spaces  "}},
			want:   "token-with-spaces",
		},
		{
			name:   "cookie fallback",
			header: withCookie(http.Header{}, "cookie-token"),
			want:   "cookie-token",
		},
		{
			name:   "bearer wins over cookie",
			header: withCookie(http.Header{"Authorization": {"Bearer header-token"}}, "cookie-token"),
			want:   "header-token",
		},
		{
			name:   "non-bearer authorization ignored, no cookie",
			header: http.Header{"Authorization": {"Basic dXNlcjpwYXNz"}},
			want:   "",
		},
		{
			name:   "empty",
			header: cookieOnly,
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenFromHeaderOrCookie(tc.header, cookieFromHeader(tc.header))
			if got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
		})
	}
}
