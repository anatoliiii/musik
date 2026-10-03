package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var ErrOIDCFlow = errors.New("invalid or expired OIDC flow")

type OIDCConfig struct{ Issuer, ClientID, ClientSecret, RedirectURL string }
type OIDCIdentity struct {
	Issuer, Subject, Name, Email string
	EmailVerified                bool
	Invitation                   string
	FlowData                     string
}
type oidcFlow struct {
	binding, nonce, verifier, invitation, data string
	expires                                    time.Time
}
type OIDCClient struct {
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
	mu       sync.Mutex
	flows    map[string]oidcFlow
}

func NewOIDCClient(ctx context.Context, cfg OIDCConfig) (*OIDCClient, error) {
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Host == "" || issuer.Scheme != "https" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("OIDC issuer must use HTTPS")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Host == "" || redirect.Scheme != "https" || redirect.User != nil || redirect.Fragment != "" || redirect.RawQuery != "" || cfg.ClientID == "" {
		return nil, errors.New("OIDC requires a client ID and fixed HTTPS redirect URL")
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	var metadata struct {
		JWKS string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, err
	}
	for _, endpoint := range []string{provider.Endpoint().AuthURL, provider.Endpoint().TokenURL, metadata.JWKS} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, errors.New("OIDC provider endpoints must use HTTPS")
		}
	}
	return &OIDCClient{config: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL, Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}}, verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}), flows: make(map[string]oidcFlow)}, nil
}

func oidcRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Begin returns the redirect and a browser binding to keep in an HttpOnly
// cookie. State, nonce, PKCE and invitation remain in server memory.
func (c *OIDCClient) Begin(invitation string) (string, string, error) {
	return c.BeginWithData(invitation, "")
}

// BeginWithData keeps trusted application state server-side and returns it
// only after the corresponding OIDC response has been verified.
func (c *OIDCClient) BeginWithData(invitation, data string) (string, string, error) {
	state, err := oidcRandom()
	if err != nil {
		return "", "", err
	}
	binding, err := oidcRandom()
	if err != nil {
		return "", "", err
	}
	nonce, err := oidcRandom()
	if err != nil {
		return "", "", err
	}
	verifier, err := oidcRandom()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, flow := range c.flows {
		if !flow.expires.After(now) {
			delete(c.flows, key)
		}
	}
	if len(c.flows) >= 1024 {
		return "", "", errors.New("OIDC flow capacity reached")
	}
	c.flows[state] = oidcFlow{binding: binding, nonce: nonce, verifier: verifier, invitation: invitation, data: data, expires: now.Add(5 * time.Minute)}
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if data != "" {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"))
	}
	return c.config.AuthCodeURL(state, options...), binding, nil
}

func (c *OIDCClient) Complete(ctx context.Context, state, binding, code string) (OIDCIdentity, error) {
	c.mu.Lock()
	flow, ok := c.flows[state]
	if !ok || !flow.expires.After(time.Now()) || subtle.ConstantTimeCompare([]byte(flow.binding), []byte(binding)) != 1 || code == "" {
		c.mu.Unlock()
		return OIDCIdentity{}, ErrOIDCFlow
	}
	delete(c.flows, state)
	c.mu.Unlock()
	token, err := c.config.Exchange(ctx, code, oauth2.VerifierOption(flow.verifier))
	if err != nil {
		return OIDCIdentity{}, err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return OIDCIdentity{}, ErrOIDCFlow
	}
	verified, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return OIDCIdentity{}, err
	}
	if subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(flow.nonce)) != 1 || verified.Subject == "" {
		return OIDCIdentity{}, ErrOIDCFlow
	}
	var claims struct {
		Name          string `json:"name"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := verified.Claims(&claims); err != nil {
		return OIDCIdentity{}, err
	}
	return OIDCIdentity{Issuer: verified.Issuer, Subject: verified.Subject, Name: claims.Name, Email: claims.Email, EmailVerified: claims.EmailVerified, Invitation: flow.invitation, FlowData: flow.data}, nil
}
