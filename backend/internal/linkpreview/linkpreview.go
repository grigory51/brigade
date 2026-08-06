package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/net/html"
)

const maxHTMLBytes = 1 << 20
const maxURLBytes = 4096

var ErrInvalidURL = errors.New("invalid link preview URL")

type Preview struct {
	URL         string
	Title       string
	Description string
	ImageURL    string
	SiteName    string
	IconURL     string
}

type Service struct {
	client *http.Client
	cache  *lru.Cache[string, Preview]
}

func New() *Service {
	cache, _ := lru.New[string, Preview](1024)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			ip := address.Unmap()
			if !publicIP(ip) {
				return nil, fmt.Errorf("link preview: private address %s", ip)
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("link preview: host has no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	return &Service{client: &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("link preview: too many redirects")
			}
			return validateURL(req.URL)
		},
	}, cache: cache}
}

func (s *Service) Get(ctx context.Context, rawURL string) (Preview, error) {
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) > maxURLBytes {
		return Preview{}, ErrInvalidURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || validateURL(u) != nil {
		return Preview{}, ErrInvalidURL
	}
	key := u.String()
	if preview, ok := s.cache.Get(key); ok {
		return preview, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return Preview{}, err
	}
	req.Header.Set("User-Agent", "Brigade Link Preview/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := s.client.Do(req)
	if err != nil {
		return Preview{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Preview{}, fmt.Errorf("link preview: upstream status %s", resp.Status)
	}
	preview, err := parse(io.LimitReader(resp.Body, maxHTMLBytes), resp.Request.URL)
	if err != nil {
		return Preview{}, err
	}
	s.cache.Add(key, preview)
	return preview, nil
}

func validateURL(u *url.URL) error {
	if u == nil || len(u.String()) > maxURLBytes || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("link preview: only public HTTP(S) URLs are allowed")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip.Unmap()) {
		return fmt.Errorf("link preview: private address %s", ip)
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func parse(r io.Reader, pageURL *url.URL) (Preview, error) {
	preview := Preview{URL: pageURL.String(), SiteName: pageURL.Hostname()}
	meta := make(map[string]string)
	var title strings.Builder
	inTitle := false
	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != nil && err != io.EOF {
				return Preview{}, err
			}
			preview.Title = limit(first(meta["og:title"], meta["twitter:title"], strings.TrimSpace(title.String()), preview.SiteName), 300)
			preview.Description = limit(first(meta["og:description"], meta["twitter:description"], meta["description"]), 1000)
			preview.SiteName = first(meta["og:site_name"], preview.SiteName)
			preview.ImageURL = resolve(pageURL, first(meta["og:image"], meta["twitter:image"]))
			preview.IconURL = resolve(pageURL, meta["icon"])
			if canonical := resolve(pageURL, meta["og:url"]); canonical != "" {
				preview.URL = canonical
			}
			return preview, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			switch strings.ToLower(token.Data) {
			case "title":
				inTitle = true
			case "meta":
				attrs := attributes(token)
				key := strings.ToLower(first(attrs["property"], attrs["name"]))
				if key != "" && attrs["content"] != "" {
					meta[key] = strings.TrimSpace(attrs["content"])
				}
			case "link":
				attrs := attributes(token)
				if strings.Contains(strings.ToLower(attrs["rel"]), "icon") && meta["icon"] == "" {
					meta["icon"] = attrs["href"]
				}
			}
		case html.EndTagToken:
			if strings.EqualFold(z.Token().Data, "title") {
				inTitle = false
			}
		case html.TextToken:
			if inTitle {
				title.Write(z.Text())
			}
		}
	}
}

func attributes(token html.Token) map[string]string {
	out := make(map[string]string, len(token.Attr))
	for _, attr := range token.Attr {
		out[strings.ToLower(attr.Key)] = attr.Val
	}
	return out
}

func first(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func limit(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func resolve(base *url.URL, raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" {
		return ""
	}
	u = base.ResolveReference(u)
	if validateURL(u) != nil {
		return ""
	}
	return u.String()
}
