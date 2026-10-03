package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestOIDCVerifiedFlow(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(v any) string {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	for _, scenario := range []string{"valid", "nonce", "audience", "issuer", "expired", "signature"} {
		t.Run(scenario, func(t *testing.T) {
			var issuer, nonce, challenge string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
				case "/keys":
					json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
				case "/token":
					r.ParseForm()
					hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
						http.Error(w, "PKCE rejected", 400)
						return
					}
					claims := map[string]any{"iss": issuer, "sub": "subject", "aud": "client", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "name": "User", "email": "u@test", "email_verified": true}
					switch scenario {
					case "nonce":
						claims["nonce"] = "wrong"
					case "audience":
						claims["aud"] = "other"
					case "issuer":
						claims["iss"] = "https://other.test"
					case "expired":
						claims["exp"] = time.Now().Add(-time.Hour).Unix()
					}
					payload := encode(map[string]any{"alg": "RS256", "kid": "test"}) + "." + encode(claims)
					digest := sha256.Sum256([]byte(payload))
					sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
					if e != nil {
						t.Error(e)
						return
					}
					if scenario == "signature" {
						sig[0] ^= 1
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": payload + "." + base64.RawURLEncoding.EncodeToString(sig)})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			issuer = server.URL
			ctx := oidc.ClientContext(context.Background(), server.Client())
			client, err := NewOIDCClient(ctx, OIDCConfig{Issuer: issuer, ClientID: "client", RedirectURL: "https://musik.test/callback"})
			if err != nil {
				t.Fatal(err)
			}
			start, binding, err := client.BeginWithData("invitation", "trusted-link-state")
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(start)
			state := parsed.Query().Get("state")
			nonce = parsed.Query().Get("nonce")
			challenge = parsed.Query().Get("code_challenge")
			if parsed.Query().Get("prompt") != "login" {
				t.Fatal("identity-link flow did not require the provider to prompt again")
			}
			if _, err := client.Complete(ctx, state, "wrong-browser", "code"); err != ErrOIDCFlow {
				t.Fatalf("foreign browser accepted: %v", err)
			}
			identity, err := client.Complete(ctx, state, binding, "code")
			if scenario == "valid" {
				if err != nil || identity.Subject != "subject" || identity.Invitation != "invitation" || identity.FlowData != "trusted-link-state" || !identity.EmailVerified {
					t.Fatalf("identity=%#v error=%v", identity, err)
				}
			} else if err == nil {
				t.Fatal("invalid token accepted")
			}
			if _, err := client.Complete(ctx, state, binding, "code"); err != ErrOIDCFlow {
				t.Fatalf("flow replay accepted: %v", err)
			}
		})
	}
}
