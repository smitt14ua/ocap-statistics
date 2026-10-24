package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// SteamEndpoint is Steam's OpenID 2.0 provider.
const SteamEndpoint = "https://steamcommunity.com/openid/login"

const (
	openIDNS         = "http://specs.openid.net/auth/2.0"
	identifierSelect = "http://specs.openid.net/auth/2.0/identifier_select"
	// nonceMaxAge is how old a positive assertion may be. Steam answers within seconds; the
	// margin covers clock skew and a slow sign-in page.
	nonceMaxAge = 10 * time.Minute
)

// steamIDPattern matches the claimed identifier Steam returns; the group is the 64-bit Steam id.
var steamIDPattern = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(\d{17})$`)

// Steam implements "Sign in through Steam" (OpenID 2.0). Steam signs its answer; the
// signature is checked by asking Steam directly (check_authentication), so no association
// or shared secret is kept.
type Steam struct {
	// Endpoint is the OpenID provider; tests replace it.
	Endpoint string
	Client   *http.Client

	mu     sync.Mutex
	nonces map[string]time.Time // used response nonces, until they expire
}

func NewSteam() *Steam {
	return &Steam{Endpoint: SteamEndpoint, Client: &http.Client{Timeout: 15 * time.Second}}
}

// LoginURL returns the Steam page to send the browser to. Steam redirects back to returnTo,
// which must lie within realm.
func (s *Steam) LoginURL(returnTo, realm string) string {
	q := url.Values{
		"openid.ns":         {openIDNS},
		"openid.mode":       {"checkid_setup"},
		"openid.return_to":  {returnTo},
		"openid.realm":      {realm},
		"openid.identity":   {identifierSelect},
		"openid.claimed_id": {identifierSelect},
	}
	return s.Endpoint + "?" + q.Encode()
}

// ErrSteamCancelled is returned when the user cancelled the sign-in on Steam.
var ErrSteamCancelled = errors.New("steam sign-in cancelled")

// Verify checks Steam's answer to LoginURL (the query of the request to returnTo) and returns
// the user's 64-bit Steam id. The answer only counts when it is meant for exactly returnTo,
// comes from the configured provider, signs every field it is trusted for, is fresh and
// unused, and Steam confirms the signature.
func (s *Steam) Verify(ctx context.Context, q url.Values, returnTo string) (string, error) {
	switch q.Get("openid.mode") {
	case "id_res":
	case "cancel":
		return "", ErrSteamCancelled
	default:
		return "", fmt.Errorf("unexpected openid.mode %q", q.Get("openid.mode"))
	}
	if q.Get("openid.ns") != openIDNS {
		return "", errors.New("not an OpenID 2.0 response")
	}
	if q.Get("openid.op_endpoint") != s.Endpoint {
		return "", fmt.Errorf("unexpected provider %q", q.Get("openid.op_endpoint"))
	}
	if q.Get("openid.return_to") != returnTo {
		return "", errors.New("openid.return_to does not match this request")
	}
	claimed := q.Get("openid.claimed_id")
	m := steamIDPattern.FindStringSubmatch(claimed)
	if m == nil || q.Get("openid.identity") != claimed {
		return "", fmt.Errorf("unexpected identity %q", claimed)
	}
	signed := strings.Split(q.Get("openid.signed"), ",")
	for _, f := range []string{"op_endpoint", "claimed_id", "identity", "return_to", "response_nonce", "assoc_handle"} {
		if !slices.Contains(signed, f) {
			return "", fmt.Errorf("field %s is not signed", f)
		}
	}
	nonce := q.Get("openid.response_nonce")
	if err := s.useNonce(nonce); err != nil {
		return "", err
	}
	if err := s.checkAuthentication(ctx, q); err != nil {
		return "", err
	}
	return m[1], nil
}

// useNonce rejects a stale or already used response nonce ("2026-10-24T12:00:00Z" + unique
// suffix), so a captured answer cannot be replayed.
func (s *Steam) useNonce(nonce string) error {
	if len(nonce) < 20 || len(nonce) > 255 {
		return errors.New("invalid response nonce")
	}
	issued, err := time.Parse(time.RFC3339, nonce[:20])
	if err != nil {
		return errors.New("invalid response nonce")
	}
	now := time.Now()
	if now.Sub(issued) > nonceMaxAge || issued.Sub(now) > nonceMaxAge {
		return errors.New("response nonce expired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces == nil {
		s.nonces = map[string]time.Time{}
	}
	for n, until := range s.nonces {
		if now.After(until) {
			delete(s.nonces, n)
		}
	}
	if _, used := s.nonces[nonce]; used {
		return errors.New("response nonce already used")
	}
	s.nonces[nonce] = issued.Add(2 * nonceMaxAge)
	return nil
}

// checkAuthentication sends the signed answer back to Steam, which confirms its signature.
func (s *Steam) checkAuthentication(ctx context.Context, q url.Values) error {
	form := url.Values{}
	for k, v := range q {
		if strings.HasPrefix(k, "openid.") {
			form[k] = v
		}
	}
	form.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("steam check_authentication: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("steam check_authentication: HTTP %d", resp.StatusCode)
	}
	// The answer is key:value lines; only "is_valid:true" confirms the signature.
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 16<<10))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "is_valid:true" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("steam check_authentication: %w", err)
	}
	return errors.New("steam did not confirm the sign-in")
}
