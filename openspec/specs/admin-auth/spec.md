# admin-auth Specification

## Purpose

Protects the remotely reachable inventory with a single owner account, server-side sessions, and browser-side request forgery defenses, so no filesystem metadata is exposed or changed without authenticated, same-origin intent.

## Requirements

### Requirement: Administrator credential set only from the local CLI
The administrator password SHALL be created or reset only by `curator admin set-password`, which reads it twice from an interactive terminal without echo. The command SHALL refuse non-terminal input and passwords shorter than 15 characters. No default password and no HTTP bootstrap endpoint SHALL exist.

#### Scenario: Fresh install has no usable login
- **WHEN** the server runs before any administrator password has been set
- **THEN** every login attempt fails, and the login page tells the operator to run `curator admin set-password`

#### Scenario: Non-interactive input refused
- **WHEN** `curator admin set-password` runs with standard input redirected from a file
- **THEN** it exits non-zero without changing the stored credential

#### Scenario: Password reset revokes sessions
- **WHEN** the administrator password is reset through the CLI
- **THEN** every existing session is revoked

### Requirement: Argon2id password storage
Passwords SHALL be stored only as Argon2id hashes, each with a random per-hash salt and its encoded, versioned work parameters. After a successful login with outdated parameters, the hash SHALL be upgraded to the current parameters.

#### Scenario: Stored credential contains no plaintext
- **WHEN** a password has been set
- **THEN** the database holds only an Argon2id encoded hash, with its parameters and salt, and never the password

#### Scenario: Parameter upgrade on login
- **WHEN** the administrator logs in successfully with a hash created under older work parameters
- **THEN** the stored hash is replaced by one using the current parameters

### Requirement: Authenticated access to everything except login
Every page, API endpoint, and event stream SHALL require a valid session, except the login page, the login submission, and the static assets the login page needs. Unauthenticated API requests SHALL receive HTTP 401. Unauthenticated page requests SHALL be redirected to the login page.

#### Scenario: A15 unauthenticated API request rejected
- **WHEN** a request without a session cookie calls `GET /api/sources`
- **THEN** the response is HTTP 401 and contains no inventory data

#### Scenario: Unauthenticated page redirected
- **WHEN** a browser without a session requests the explorer page
- **THEN** it is redirected to the login page

### Requirement: Opaque server-side sessions
Sessions SHALL be random opaque tokens of at least 256 bits, stored server-side only as digests. A new session SHALL be issued on every successful login. Sessions SHALL expire after configurable idle and absolute lifetimes, and logout SHALL revoke the session immediately.

#### Scenario: Session rotation on login
- **WHEN** a client holding a pre-login cookie logs in successfully
- **THEN** the response sets a new session token, and the pre-login token no longer authenticates

#### Scenario: Idle expiry
- **WHEN** a session is unused for longer than the idle lifetime
- **THEN** the next request with it is treated as unauthenticated

#### Scenario: Logout revokes
- **WHEN** the administrator logs out and the old cookie is replayed
- **THEN** the replayed request is treated as unauthenticated

### Requirement: Hardened session cookie
The session cookie SHALL be `HttpOnly`, `SameSite=Strict`, and scoped to path `/`. When the external origin uses HTTPS, the cookie SHALL also be `Secure` and use the `__Host-` name prefix.

#### Scenario: HTTPS origin cookie attributes
- **WHEN** the external origin is `https://curator.example.net` and login succeeds
- **THEN** the `Set-Cookie` header carries the `__Host-` prefix and the `Secure`, `HttpOnly`, and `SameSite=Strict` attributes

### Requirement: Cross-site request forgery protection
Every state-changing request, including login and logout, SHALL carry a valid CSRF token bound to the current session or pre-login session. Its `Origin` header SHALL equal the configured external origin; when `Origin` is absent, `Referer` SHALL match that origin. Requests failing either check SHALL receive HTTP 403 without side effects.

#### Scenario: A15 cross-site command rejected
- **WHEN** an authenticated browser sends `POST /api/commands/start-scan` with `Origin: https://evil.example`
- **THEN** the response is HTTP 403 and no job is created

#### Scenario: A15 missing CSRF token rejected
- **WHEN** a same-origin authenticated request omits the CSRF token
- **THEN** the response is HTTP 403 and no job is created

#### Scenario: Null origin rejected
- **WHEN** a state-changing request carries `Origin: null` and a valid CSRF token
- **THEN** the response is HTTP 403

#### Scenario: Same-origin form post accepted
- **WHEN** a browser form posts to `/login` with `Origin` equal to the external origin and a valid login CSRF token
- **THEN** the request passes the forgery checks

### Requirement: Login throttling
Failed login attempts SHALL be throttled per client address and globally for the single account. Delays SHALL grow with consecutive failures up to a configured maximum lockout window. Every failed or throttled attempt SHALL produce the same generic error and an audit event.

#### Scenario: Repeated failures throttled
- **WHEN** a client submits ten wrong passwords in quick succession
- **THEN** later attempts are refused with a generic error before password verification runs, until the backoff window elapses

### Requirement: Request limits and security headers
Request bodies above the configured limit SHALL be rejected with HTTP 413 before full processing. Every response SHALL carry a restrictive Content-Security-Policy that permits only same-origin scripts, styles, images, and connections, forbids framing, and bans inline script. Every response SHALL also carry `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`, which sends no referrer to other origins while keeping the real `Origin` on same-origin form posts. No permissive CORS headers SHALL be sent.

#### Scenario: Oversized body
- **WHEN** a command request body exceeds the configured limit
- **THEN** the response is HTTP 413 and the command is not executed

#### Scenario: Headers on every response
- **WHEN** any page or API response is returned
- **THEN** it includes the CSP, nosniff, and referrer headers, and no `Access-Control-Allow-Origin` header

### Requirement: Security audit trail
Login successes, login failures, logouts, password changes, and session revocations SHALL be recorded as audit events. Each event SHALL carry a timestamp and the derived client address, and SHALL never contain passwords or session tokens.

#### Scenario: Failed login audited
- **WHEN** a login attempt fails
- **THEN** an audit event records the failure, the time, and the client address, but no submitted password
