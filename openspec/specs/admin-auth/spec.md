# admin-auth Specification

## Purpose

Protects the remotely reachable inventory with a single owner account, server-side sessions, and browser-side request forgery defenses, so no filesystem metadata is exposed or changed without authenticated, same-origin intent.

## Requirements

### Requirement: Administrator credential set only from the local CLI
The administrator password SHALL be created or reset only by `precious admin set-password`, which reads it twice from an interactive terminal without echo. The command SHALL refuse non-terminal input and passwords shorter than 15 characters. No default password and no HTTP bootstrap endpoint SHALL exist.

#### Scenario: Fresh install has no usable login
- **WHEN** the server runs before any administrator password has been set
- **THEN** every login attempt fails, `GET /api/session` reports `admin_exists` as false, and the login screen tells the operator to run `precious admin set-password`

#### Scenario: Non-interactive input refused
- **WHEN** `precious admin set-password` runs with standard input redirected from a file
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
Every API endpoint and the event stream SHALL require a valid session, except `GET /api/session`, `POST /api/session/login`, the application shell (served for any non-`/api` path), and its hashed static assets. Unauthenticated `/api` requests SHALL receive HTTP 401 with a JSON error body. Without a session, the application shell SHALL show the login screen.

#### Scenario: A15 unauthenticated API request rejected
- **WHEN** a request without a session cookie calls `GET /api/sources`
- **THEN** the response is HTTP 401 with a JSON error body and contains no inventory data

#### Scenario: Unauthenticated page redirected
- **WHEN** a browser without a session opens a Map address such as `/map/12`
- **THEN** it receives the application shell, which redirects to the login screen and shows no inventory data

#### Scenario: Session bootstrap is public
- **WHEN** a browser without a session calls `GET /api/session`
- **THEN** the response is HTTP 200 and reports `authenticated` as false

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
- **WHEN** the login screen posts to `POST /api/session/login` with `Origin` equal to the external origin and the pre-login session's CSRF token in the `X-CSRF-Token` header
- **THEN** the request passes the forgery checks

### Requirement: Login throttling
Failed login attempts SHALL be throttled per client address and globally for the single account. Delays SHALL grow with consecutive failures up to a configured maximum lockout window. Every failed or throttled attempt SHALL produce the same generic error and an audit event.

#### Scenario: Repeated failures throttled
- **WHEN** a client submits ten wrong passwords in quick succession
- **THEN** later attempts are refused with a generic error before password verification runs, until the backoff window elapses

### Requirement: Request limits and security headers
Request bodies above the configured limit SHALL be rejected with HTTP 413 before full processing. Application responses SHALL carry a Content-Security-Policy that permits only same-origin scripts, styles, images, media, frames, and connections, forbids framing the application, and bans inline script and style, plugins, and base URIs. File content responses SHALL carry their own stricter policy (file-viewer capability). Every response SHALL carry `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`, and no permissive CORS headers.

#### Scenario: Oversized body
- **WHEN** a command request body exceeds the configured limit
- **THEN** the response is HTTP 413 and the command is not executed

#### Scenario: Headers on every response
- **WHEN** the application shell, a static asset, or an API response other than file content is returned
- **THEN** it includes the application CSP, nosniff, and referrer headers, and no `Access-Control-Allow-Origin` header

#### Scenario: Application cannot be framed
- **WHEN** another page, even one from the same origin, tries to frame the application shell
- **THEN** the browser refuses, because the policy forbids framing the application

#### Scenario: Content responses use their own policy
- **WHEN** `GET /api/entries/{id}/content` returns a file
- **THEN** the response carries the viewer's stricter policy instead of the application CSP, together with nosniff and no CORS headers

### Requirement: Security audit trail
Login successes, login failures, logouts, password changes, and session revocations SHALL be recorded as audit events. Each event SHALL carry a timestamp and the derived client address, and SHALL never contain passwords or session tokens.

#### Scenario: Failed login audited
- **WHEN** a login attempt fails
- **THEN** an audit event records the failure, the time, and the client address, but no submitted password

### Requirement: Session bootstrap for the single-page app
`GET /api/session` SHALL return `authenticated`, `csrf_token`, and `admin_exists`, creating a pre-login session when there is none. `POST /api/session/login` with `{password}` SHALL return 200 `{authenticated: true, csrf_token}`, or 401 `login_failed` with a generic message. `POST /api/session/logout` SHALL return 204 and revoke the session. Each POST SHALL pass the forgery checks.

#### Scenario: First visit gets a pre-login session
- **WHEN** a browser with no cookie calls `GET /api/session`
- **THEN** the response sets a pre-login session cookie and returns `authenticated` false, a `csrf_token`, and `admin_exists`

#### Scenario: Successful login
- **WHEN** the app posts the correct password to `POST /api/session/login` with the pre-login CSRF token
- **THEN** the response is HTTP 200 with `authenticated` true and a new `csrf_token`, and a new session cookie is set

#### Scenario: Failed login is generic
- **WHEN** the app posts a wrong password, or any login is attempted before an administrator exists
- **THEN** the response is HTTP 401 with code `login_failed` and a message that does not say which check failed

#### Scenario: Logout
- **WHEN** an authenticated app calls `POST /api/session/logout` with its CSRF token
- **THEN** the response is HTTP 204, and the old session cookie no longer authenticates

#### Scenario: Login without CSRF token
- **WHEN** `POST /api/session/login` arrives without the `X-CSRF-Token` header
- **THEN** the response is HTTP 403 and no password is verified

### Requirement: Cookie names
The session cookie SHALL be named `__Host-precious_session` when the external origin uses HTTPS, and `precious_session` when it uses plain HTTP.

#### Scenario: HTTPS origin
- **WHEN** the external origin is `https://precious.example.net` and a session is created
- **THEN** the cookie is named `__Host-precious_session`

#### Scenario: Plain HTTP origin
- **WHEN** the external origin is `http://nas.local:8080` and a session is created
- **THEN** the cookie is named `precious_session`
