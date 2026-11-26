package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type endpoint struct {
	Endpoint
	client       *http.Client
	mu           sync.Mutex
	blockedUntil map[string]time.Time
}

func newEndpoint(cfg Endpoint) (*endpoint, error) {
	base, err := url.Parse(cfg.APIURL)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" || strings.Contains(base.Path, "..") {
		return nil, errors.New("coding: invalid API URL")
	}
	if base.Scheme != "https" {
		return nil, errors.New("coding: API requires HTTPS")
	}
	if cfg.Provider != GitHub && cfg.Provider != GitLab {
		return nil, errors.New("coding: unsupported provider")
	}
	cfg.RemoteHost = strings.ToLower(cfg.RemoteHost)
	if cfg.RemoteHost == "" || strings.ContainsAny(cfg.RemoteHost, "/:@?#% ") {
		return nil, errors.New("coding: invalid remote_host")
	}
	if cfg.Provider == GitLab && cfg.TokenSecret == "" {
		return nil, errors.New("coding: GitLab token_secret required")
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	var allowed []netip.Prefix
	for _, raw := range cfg.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, errors.New("coding: invalid allowed CIDR")
		}
		allowed = append(allowed, prefix)
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 8
	transport.MaxResponseHeaderBytes = 32 << 10
	transport.ResponseHeaderTimeout = 5 * time.Second
	transport.TLSHandshakeTimeout = 3 * time.Second
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, base.Hostname()) {
			return nil, ErrUpstream
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, ErrUpstream
		}
		for _, ip := range addresses {
			if !allowedAddress(ip.Unmap(), allowed) {
				return nil, ErrUpstream
			}
		}
		// Dial the validated IP, not the hostname, to prevent DNS rebinding.
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	return &endpoint{Endpoint: cfg, blockedUntil: make(map[string]time.Time), client: &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func allowedAddress(ip netip.Addr, allowed []netip.Prefix) bool {
	// Metadata, link-local, multicast and unspecified addresses are never valid CI endpoints.
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, prefix := range allowed {
		if prefix.Contains(ip) {
			return true
		}
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
		return false
	}
	for _, prefix := range blockedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

type providerRequest struct {
	endpoint  *endpoint
	token     string
	userID    string
	count     int
	bytesRead int
}

func (r *providerRequest) get(ctx context.Context, path string, query url.Values, target any) (bool, error) {
	const maxResponse = 512 << 10
	const maxTotalResponse = 2 << 20
	limit := min(maxResponse, maxTotalResponse-r.bytesRead)
	if limit <= 0 {
		return false, ErrUpstream
	}
	if r.count >= 12 {
		return false, ErrRateLimited
	}
	r.count++
	ep := r.endpoint
	principal := r.userID
	if r.token == "" {
		principal = ""
	} else if principal == "" {
		return false, ErrInvalidRequest
	}
	ep.mu.Lock()
	for key, until := range ep.blockedUntil {
		if !time.Now().Before(until) {
			delete(ep.blockedUntil, key)
		}
	}
	blocked := time.Now().Before(ep.blockedUntil[principal])
	ep.mu.Unlock()
	if blocked {
		return false, ErrRateLimited
	}
	// All paths are assembled locally; upstream URL fields and Link are never followed.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.APIURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return false, ErrUpstream
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Brigade-CI")
	if ep.Provider == GitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
	} else {
		req.Header.Set("PRIVATE-TOKEN", r.token)
	}
	response, err := ep.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "") {
		until := time.Now().Add(time.Minute)
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			until = time.Now().Add(time.Duration(min(seconds, 3600)) * time.Second)
		} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil && date.After(until) {
			until = date
		}
		if reset, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(reset, 0).After(until) {
			until = time.Unix(reset, 0)
		}
		if limit := time.Now().Add(time.Hour); until.After(limit) {
			until = limit
		}
		ep.mu.Lock()
		if until.After(ep.blockedUntil[principal]) && (len(ep.blockedUntil) < 1024 || !ep.blockedUntil[principal].IsZero()) {
			ep.blockedUntil[principal] = until
		}
		ep.mu.Unlock()
		return false, ErrRateLimited
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return false, ErrCredentials
	}
	if response.StatusCode != 200 {
		return false, fmt.Errorf("%w (HTTP %d)", ErrUpstream, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	r.bytesRead += len(data)
	if err != nil || len(data) > limit {
		return false, ErrUpstream
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, ErrUpstream
	}
	return response.Header.Get("X-Next-Page") != "" || strings.Contains(response.Header.Get("Link"), `rel="next"`), nil
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	value := u.String()
	if len(value) > 2048 {
		return ""
	}
	return strings.Clone(value)
}
