# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's
[security advisories](https://github.com/smitt14ua/ocap-statistics/security/advisories/new)
instead of opening a public issue. Include the affected version, steps to reproduce and the
impact you expect. You should get an answer within a week.

## Deployment notes

- Run the service behind a reverse proxy that terminates TLS, and set
  `OCAPSTATS_TRUST_PROXY=true` only when the service is not reachable directly. Client
  addresses (login rate limiting) and HTTPS detection (secure cookies, HSTS) rely on it.
- Use a strong `OCAPSTATS_ADMIN_PASSWORD` and remove it from the environment once you have
  changed the password in the admin panel, or keep `OCAPSTATS_ADMIN_PASSWORD_RESET` unset.
- `OCAPSTATS_ADMIN_TOKEN` grants full admin access; treat it like a password.
- Mount OCAP's data directory read-only; the service never writes to it.

## Built-in protections

- Admin passwords are stored as PBKDF2-HMAC-SHA256 (600 000 iterations); session tokens are
  stored hashed and sent as `HttpOnly`, `SameSite=Strict` cookies (`__Host-` prefixed over HTTPS).
- Steam sign-in (OpenID 2.0) accepts an answer only when Steam confirms its signature
  (`check_authentication`), it comes from Steam's endpoint, signs the identity, return address
  and nonce, is addressed to exactly the sign-in started by this browser (a state cookie), and
  its nonce is fresh and unused.
- OpenID Connect sign-in uses the Authorization Code flow with PKCE (`S256`), a state cookie
  and a nonce; the ID token is verified by go-oidc (signature against the provider's JWKS,
  issuer, audience, expiry), the userinfo must name the same `sub`, and users are bound by
  issuer and `sub` only. The back-channel logout endpoint (called by the provider's server,
  so without CSRF check or authentication) accepts only a logout token signed with the
  provider's keys, for this client, with the logout event, a recent `iat`, no `nonce` and an
  unused `jti`. Codes, tokens and profile data are never logged; the ID token is kept
  server-side only as `id_token_hint` for the logout. Player sessions are stored hashed, sent as `HttpOnly`,
  `SameSite=Lax` cookies, grant no admin access, and only reveal the player's own Steam id.
- Failed logins are rate limited per client address and per user name; password hashing is
  bounded so it cannot exhaust the CPU.
- State-changing admin requests require a custom header (CSRF protection) and the admin API
  sends no CORS headers.
- Pages are served with a strict Content-Security-Policy (no inline scripts, no `eval`),
  `X-Content-Type-Options`, `Referrer-Policy`, and the admin panel cannot be framed.
  Third-party scripts are pinned with Subresource Integrity.
