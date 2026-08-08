package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var ErrRequiredRole = errors.New("auth: required OIDC role is missing")

type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	RoleClaim     string
	RequiredRole  string
	UsernameClaim string
}

type oidcFlow struct {
	verifier        string
	nonce           string
	returnTo        string
	desktopCallback string
	expires         time.Time
}

type oidcHandoff struct {
	pair    TokenPair
	expires time.Time
}

type OIDC struct {
	service  *Service
	config   OIDCConfig
	oauth    oauth2.Config
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier

	mu       sync.Mutex
	flows    map[string]oidcFlow
	handoffs map[string]oidcHandoff
}

func NewOIDC(ctx context.Context, service *Service, config OIDCConfig) (*OIDC, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery: %w", err)
	}
	return &OIDC{
		service: service,
		config:  config,
		oauth: oauth2.Config{
			ClientID: config.ClientID, ClientSecret: config.ClientSecret,
			Endpoint: provider.Endpoint(), RedirectURL: config.RedirectURL, Scopes: config.Scopes,
		},
		provider: provider, verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID}),
		flows: map[string]oidcFlow{}, handoffs: map[string]oidcHandoff{},
	}, nil
}

func (o *OIDC) Start(returnTo, desktopCallback string) (string, error) {
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	o.mu.Lock()
	o.evictLocked()
	o.flows[state] = oidcFlow{verifier: verifier, nonce: nonce, returnTo: returnTo, desktopCallback: desktopCallback, expires: time.Now().Add(10 * time.Minute)}
	o.mu.Unlock()
	return o.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (o *OIDC) Complete(ctx context.Context, state, code string) (TokenPair, string, string, error) {
	o.mu.Lock()
	flow, ok := o.flows[state]
	delete(o.flows, state)
	o.mu.Unlock()
	if !ok || time.Now().After(flow.expires) || code == "" {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, ErrInvalidToken
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.verifier))
	if err != nil {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, fmt.Errorf("auth: OIDC token exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, errors.New("auth: OIDC response has no id_token")
	}
	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, fmt.Errorf("auth: verify ID token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.nonce)) != 1 {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, errors.New("auth: invalid OIDC nonce")
	}

	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, fmt.Errorf("auth: decode ID token: %w", err)
	}
	if userInfo, err := o.userInfo(ctx, token); err == nil {
		for key, value := range userInfo {
			if _, exists := claims[key]; !exists {
				claims[key] = value
			}
		}
	}
	if !containsRole(claims[o.config.RoleClaim], o.config.RequiredRole) {
		return TokenPair{}, flow.returnTo, flow.desktopCallback, ErrRequiredRole
	}
	username, _ := claims[o.config.UsernameClaim].(string)
	if username == "" {
		for _, key := range []string{"preferred_username", "email", "name"} {
			if value, ok := claims[key].(string); ok && value != "" {
				username = value
				break
			}
		}
	}
	pair, err := o.service.LoginExternal(ctx, o.config.Issuer, idToken.Subject, username)
	return pair, flow.returnTo, flow.desktopCallback, err
}

func (o *OIDC) userInfo(ctx context.Context, token *oauth2.Token) (map[string]any, error) {
	response, err := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	if err := response.Claims(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func (o *OIDC) CreateHandoff(pair TokenPair) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	o.mu.Lock()
	o.evictLocked()
	o.handoffs[code] = oidcHandoff{pair: pair, expires: time.Now().Add(time.Minute)}
	o.mu.Unlock()
	return code, nil
}

func (o *OIDC) ExchangeHandoff(code string) (TokenPair, error) {
	o.mu.Lock()
	handoff, ok := o.handoffs[code]
	delete(o.handoffs, code)
	o.mu.Unlock()
	if !ok || time.Now().After(handoff.expires) {
		return TokenPair{}, ErrInvalidToken
	}
	return handoff.pair, nil
}

func (o *OIDC) evictLocked() {
	now := time.Now()
	for state, flow := range o.flows {
		if now.After(flow.expires) {
			delete(o.flows, state)
		}
	}
	for code, handoff := range o.handoffs {
		if now.After(handoff.expires) {
			delete(o.handoffs, code)
		}
	}
}

func containsRole(value any, role string) bool {
	switch value := value.(type) {
	case string:
		return value == role
	case []any:
		for _, item := range value {
			if containsRole(item, role) {
				return true
			}
		}
	case map[string]any:
		if _, ok := value[role]; ok {
			return true
		}
		for _, item := range value {
			if containsRole(item, role) {
				return true
			}
		}
	case json.RawMessage:
		var decoded any
		return json.Unmarshal(value, &decoded) == nil && containsRole(decoded, role)
	}
	return false
}
