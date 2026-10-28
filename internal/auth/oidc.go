package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/smitt14ua/ocap-statistics/internal/config"
)

const (
	maxPictureURL = 2048
	maxNameRunes  = 64
)

// steamID64Pattern matches a 64-bit Steam id.
var steamID64Pattern = regexp.MustCompile(`^\d{17}$`)

// OIDC implements the sign-in through an OpenID Connect provider: Authorization Code flow
// with PKCE (S256). Discovery, the token exchange and ID token verification are left to
// go-oidc and x/oauth2; this type adds the claims the site needs (Steam id, avatar, nickname).
type OIDC struct {
	cfg    config.OIDC
	Client *http.Client

	mu sync.Mutex
	p  *oidcProvider // discovered on first use, so a provider that is down does not stop the service
}

type oidcProvider struct {
	provider   *oidc.Provider
	verifier   *oidc.IDTokenVerifier
	oauth      oauth2.Config // without RedirectURL, which comes from the request
	endSession string
}

func NewOIDC(cfg config.OIDC) *OIDC {
	return &OIDC{cfg: cfg, Client: &http.Client{Timeout: 15 * time.Second}}
}

func (o *OIDC) ctx(ctx context.Context) context.Context { return oidc.ClientContext(ctx, o.Client) }

// provider returns the discovered provider; a failed discovery is tried again next time.
func (o *OIDC) provider(ctx context.Context) (*oidcProvider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.p != nil {
		return o.p, nil
	}
	p, err := oidc.NewProvider(o.ctx(ctx), o.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	var meta struct {
		EndSession  string   `json:"end_session_endpoint"`
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if u, err := url.Parse(meta.EndSession); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		meta.EndSession = ""
	}
	ep := p.Endpoint()
	ep.AuthStyle = authStyle(o.cfg.ClientSecret, meta.AuthMethods)
	o.p = &oidcProvider{
		provider: p,
		verifier: p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret,
			Endpoint: ep, Scopes: o.cfg.Scopes,
		},
		endSession: meta.EndSession,
	}
	return o.p, nil
}

// authStyle picks client authentication at the token endpoint: none for a public client
// (client_id in the form, PKCE proves the caller), otherwise client_secret_basic, the
// default of the specification, unless the provider only supports client_secret_post.
func authStyle(secret string, supported []string) oauth2.AuthStyle {
	switch {
	case secret == "":
		return oauth2.AuthStyleInParams
	case len(supported) == 0 || slices.Contains(supported, "client_secret_basic"):
		return oauth2.AuthStyleInHeader
	case slices.Contains(supported, "client_secret_post"):
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

// OIDCLogin holds what binds the provider's answer to the sign-in this browser started. It
// is kept in a cookie of that browser until the callback.
type OIDCLogin struct {
	State, Nonce, Verifier string
}

func NewOIDCLogin() (OIDCLogin, error) {
	state, _, err := NewToken()
	if err != nil {
		return OIDCLogin{}, err
	}
	nonce, _, err := NewToken()
	if err != nil {
		return OIDCLogin{}, err
	}
	return OIDCLogin{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}, nil
}

// AuthURL returns the provider's sign-in page for l; the provider returns to redirectURI.
func (o *OIDC) AuthURL(ctx context.Context, l OIDCLogin, redirectURI string) (string, error) {
	p, err := o.provider(ctx)
	if err != nil {
		return "", err
	}
	c := p.oauth
	c.RedirectURL = redirectURI
	return c.AuthCodeURL(l.State, oauth2.S256ChallengeOption(l.Verifier), oidc.Nonce(l.Nonce)), nil
}

// OIDCProfile is a signed-in user. Issuer and Subject identify them; the other fields are
// profile data that may change at the provider and are refreshed at every sign-in.
type OIDCProfile struct {
	Issuer, Subject string
	// SteamID is empty when the user has not linked a Steam account at the provider.
	SteamID string
	Name    string
	// Picture is an https URL of the avatar, or empty.
	Picture string
	// IDToken is the raw ID token, kept only as id_token_hint for the logout.
	IDToken string
	// Granted is the scope the token endpoint reported, empty when it reported none (which
	// means the requested scope was granted); Dropped are requested scopes it left out.
	Granted string
	Dropped []string
}

// ClaimsError means the provider did not share the claims the site needs, typically because
// it silently granted fewer scopes than requested.
type ClaimsError struct {
	Missing []string
	Granted string
	Dropped []string
}

func (e *ClaimsError) Error() string {
	return "oidc: the provider returned no " + strings.Join(e.Missing, ", ")
}

// Exchange redeems the authorization code of the sign-in l and returns the user. The ID
// token is verified (signature, issuer, audience, expiry, nonce, at_hash); profile claims
// come from the userinfo endpoint, which is up to date, and from the ID token only when the
// provider has no userinfo endpoint.
func (o *OIDC) Exchange(ctx context.Context, l OIDCLogin, redirectURI, code string) (*OIDCProfile, error) {
	p, err := o.provider(ctx)
	if err != nil {
		return nil, err
	}
	ctx = o.ctx(ctx)
	c := p.oauth
	c.RedirectURL = redirectURI
	tok, err := c.Exchange(ctx, code, oauth2.VerifierOption(l.Verifier))
	if err != nil {
		return nil, tokenError(err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, errors.New("oidc: the token response has no id_token")
	}
	idt, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(l.Nonce)) != 1 {
		return nil, errors.New("oidc: id_token nonce does not match this sign-in")
	}
	if idt.AccessTokenHash != "" {
		if err := idt.VerifyAccessToken(tok.AccessToken); err != nil {
			return nil, err
		}
	}
	claims := map[string]json.RawMessage{}
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: id_token claims: %w", err)
	}
	if p.provider.UserInfoEndpoint() != "" {
		ui, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		if err != nil {
			return nil, fmt.Errorf("oidc userinfo: %w", err)
		}
		// OpenID Connect Core 5.3.2: the userinfo must be about the user of the ID token.
		if ui.Subject != idt.Subject {
			return nil, errors.New("oidc: userinfo sub does not match the id_token")
		}
		// Only the userinfo then: a claim it leaves out (an avatar removed) must not come
		// back from the ID token.
		clear(claims)
		if err := ui.Claims(&claims); err != nil {
			return nil, fmt.Errorf("oidc userinfo: %w", err)
		}
	}

	prof := &OIDCProfile{
		Issuer: idt.Issuer, Subject: idt.Subject, IDToken: rawID,
		SteamID: steamIDClaim(claims[o.cfg.SteamIDClaim]),
		Picture: pictureClaim(claims[o.cfg.PictureClaim]),
	}
	for _, n := range o.nameClaims() {
		if prof.Name = nameClaim(claims[n]); prof.Name != "" {
			break
		}
	}
	prof.Granted, prof.Dropped = grantedScopes(tok, o.cfg.Scopes)
	// Without a Steam id the user may simply not have linked Steam, unless the provider
	// dropped scopes or shared nothing at all: then it is the client's permissions, not the
	// user, and an empty profile would only hide that.
	if prof.SteamID == "" && (len(prof.Dropped) > 0 || prof.Name == "") {
		missing := []string{o.cfg.SteamIDClaim}
		if prof.Name == "" {
			missing = append(missing, o.cfg.NameClaim)
		}
		return nil, &ClaimsError{Missing: missing, Granted: prof.Granted, Dropped: prof.Dropped}
	}
	return prof, nil
}

// LogoutURL returns the provider's end_session_endpoint with id_token_hint and
// post_logout_redirect_uri, or "" when the provider has none.
func (o *OIDC) LogoutURL(ctx context.Context, idToken, postLogout string) (string, error) {
	p, err := o.provider(ctx)
	if err != nil || p.endSession == "" {
		return "", err
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	}
	q.Set("client_id", o.cfg.ClientID)
	q.Set("post_logout_redirect_uri", postLogout)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// tokenError describes a failed token request without the response body.
func tokenError(err error) error {
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Errorf("oidc token endpoint: HTTP %d %s %s", status, re.ErrorCode, re.ErrorDescription)
	}
	return fmt.Errorf("oidc token endpoint: %w", err)
}

// nameClaims is the configured nickname claim followed by the standard fallbacks.
func (o *OIDC) nameClaims() []string {
	out := []string{o.cfg.NameClaim}
	for _, n := range []string{"preferred_username", "nickname"} {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// grantedScopes reads the scope of the token response (RFC 6749 5.1: omitted when it is the
// requested one) and the requested scopes missing from it.
func grantedScopes(tok *oauth2.Token, requested []string) (string, []string) {
	granted, _ := tok.Extra("scope").(string)
	if strings.TrimSpace(granted) == "" {
		return "", nil
	}
	have := strings.Fields(granted)
	var dropped []string
	for _, s := range requested {
		if !slices.Contains(have, s) {
			dropped = append(dropped, s)
		}
	}
	return granted, dropped
}

// steamIDClaim accepts a SteamID64 as a string or as a JSON number (read as written, since
// 17 digits do not fit a float64).
func steamIDClaim(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	if s = strings.TrimSpace(s); steamID64Pattern.MatchString(s) {
		return s
	}
	return ""
}

// pictureClaim accepts an absolute https URL: the page's CSP loads images over https only.
func pictureClaim(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil || len(s) > maxPictureURL {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func nameClaim(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > maxNameRunes {
		s = string(r[:maxNameRunes])
	}
	return strings.TrimSpace(s)
}
