package linkpreview

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGetParsesOpenGraphAndCaches(t *testing.T) {
	var requests atomic.Int32
	const cover = "\x89PNG\r\n\x1a\ncover"
	const icon = "\x00\x00\x01\x00icon"
	svc := New()
	svc.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := map[string]string{
			"/page":      `<html><head><title>Fallback</title><meta property="og:title" content="Brigade"><meta property="og:description" content="Coding agents"><meta property="og:image" content="/cover.png"><link rel="icon" href="/favicon.ico"></head></html>`,
			"/cover.png": cover, "/favicon.ico": icon,
		}[req.URL.Path]
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})

	for range 2 {
		preview, err := svc.Get(context.Background(), "https://example.com/page")
		if err != nil {
			t.Fatal(err)
		}
		if preview.Title != "Brigade" || preview.Description != "Coding agents" || preview.ImageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte(cover)) ||
			preview.IconURL != "data:"+http.DetectContentType([]byte(icon))+";base64,"+base64.StdEncoding.EncodeToString([]byte(icon)) {
			t.Fatalf("preview = %+v", preview)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func TestUnavailableImagesKeepTextWithoutExternalFallback(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing favicon", "<html>Not found</html>", 404},
		{"HTML labelled as image", "<html>Not an image</html>", 200},
		{"SVG", "<svg></svg>", 200},
		{"oversize", "GIF89a" + strings.Repeat("x", maxImageBytes), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New()
			svc.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, status := tc.body, tc.status
				if req.URL.Path == "/page" {
					body, status = `<title>Страница</title><meta property="og:image" content="/asset"><link rel="icon" href="/asset">`, 200
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			p, err := svc.Get(context.Background(), "https://example.com/page")
			if err != nil || p.Title != "Страница" || p.ImageURL != "" || p.IconURL != "" {
				t.Fatalf("preview = %+v, err = %v", p, err)
			}
		})
	}
}

func TestPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("%s разрешён", raw)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Error("публичный адрес запрещён")
	}
}

func TestValidateURLRejectsPrivateLiteral(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1/secret")
	if err != nil {
		t.Fatal(err)
	}
	if validateURL(u) == nil {
		t.Fatal("private URL is allowed")
	}
}
