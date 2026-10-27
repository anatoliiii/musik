package yandex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DeviceCode is the code the user enters at VerificationURL.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// ErrPending means the user has not confirmed the device code yet.
var ErrPending = errors.New("yandex: authorization pending")

func (c *Client) oauthPost(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.oauth()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "authorization_pending" {
			return ErrPending
		}
		return &APIError{Status: res.StatusCode, Name: e.Error, Message: e.Description}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// RequestDeviceCode starts the OAuth device flow.
func (c *Client) RequestDeviceCode(ctx context.Context, deviceName string) (DeviceCode, error) {
	var id [5]byte
	_, _ = rand.Read(id[:])
	var out DeviceCode
	err := c.oauthPost(ctx, "/device/code", url.Values{
		"client_id": {oauthClientID}, "device_id": {hex.EncodeToString(id[:])}, "device_name": {deviceName},
	}, &out)
	if err == nil && out.DeviceCode == "" {
		err = errors.New("yandex: empty device code")
	}
	return out, err
}

// PollDeviceToken exchanges a confirmed device code for a token. It returns
// ErrPending until the user confirms.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	err := c.oauthPost(ctx, "/token", url.Values{
		"grant_type": {"device_code"}, "code": {deviceCode},
		"client_id": {oauthClientID}, "client_secret": {oauthClientSecret},
	}, &out)
	if err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("yandex: empty token")
	}
	return out.AccessToken, nil
}

// RevokeToken asks Yandex to forget the token. Logout does not depend on it.
func (c *Client) RevokeToken(ctx context.Context) error {
	return c.oauthPost(ctx, "/revoke_token", url.Values{
		"access_token": {c.Token}, "client_id": {oauthClientID}, "client_secret": {oauthClientSecret},
	}, nil)
}

// Session is one musik user's Yandex sign-in.
type Session struct {
	Token       string    `json:"token"`
	Login       string    `json:"login"`
	DisplayName string    `json:"display_name"`
	HasPlus     bool      `json:"has_plus"`
	SavedAt     time.Time `json:"saved_at"`
}

// TokenStore keeps each musik user's token in its own 0600 file, so the
// plugin needs no database table and logout is a file removal.
type TokenStore struct {
	Dir string
	mu  sync.Mutex
}

func (s *TokenStore) path(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:16])+".json")
}

func (s *TokenStore) Load(userID string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path(userID))
	if err != nil {
		return Session{}, false
	}
	var out Session
	if json.Unmarshal(raw, &out) != nil || out.Token == "" {
		return Session{}, false
	}
	return out, true
}

func (s *TokenStore) Save(userID string, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(userID))
}

func (s *TokenStore) Delete(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(userID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
