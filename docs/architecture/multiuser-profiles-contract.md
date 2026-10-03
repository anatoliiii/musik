# Multi-user identity and profile contract (proposal)

Status: design only. No application behavior changes are part of this branch.

This contract separates a person who can sign in (`User`) from listening state (`Profile`). One user may own multiple independent profiles. Identity providers authenticate users; they do not own musik data or define profile semantics.

## Current boundary

- `player/internal/auth` supports one configured password and one shared API token. The signed `musik_session` cookie carries expiry, not-before and a random token, but no user identity (`player/internal/auth/auth.go`).
- `player/internal/api` exposes one global profile and uses a single `db.Store`, taste profile and playback engine (`player/internal/api/server.go`, `player/internal/api/profile.go`).
- `play_sessions`, playlists, listening history, favorites, later items, radio rules, taste state and contexts have no account or profile owner today (`player/internal/db/*.go`, Python schema/migrations in `src/musik/db/`).
- No OIDC/Keycloak integration exists in upstream code. The temporary deployment integration is configuration outside this repository and is not a trusted application contract.

## Domain contract

### User and identity

- `UserID` is an application-generated, opaque UUID represented as text. It is stable across providers and database engines.
- A user has status `active | disabled`, a display name, timestamps, and local musik roles. Roles are authorization policy, not identity-provider claims copied blindly.
- `ExternalIdentity` is keyed by `(issuer, subject)` from a verified OpenID Connect ID token and references exactly one `UserID`. Enforce uniqueness on `(issuer, subject)`.
- Email, username, and display name are mutable attributes. They are never identity keys and are never used to silently merge accounts.
- Linking another OIDC identity to an existing user requires an authenticated, explicit linking flow. First-login provisioning is controlled by server policy (closed by default; allowlist/invite or explicit JIT provisioning).
- Do not trust user IDs, roles, email, or profile IDs supplied in request bodies or proxy headers. The authenticated principal is constructed by server middleware.

### Profile

- `ProfileID` is an application-generated UUID. Every profile has exactly one owning `UserID`; every user has at least one active profile and exactly one default profile.
- A profile contains independent taste, listening history, recommendation impressions, radio preferences/rules, contexts, favorites, listen-later items, user playlists and playback sessions.
- A user may have several profiles (for example, personal, family-shared listening or a child's profile). Limits and public sharing of a profile are deferred policies, not implicit behavior.
- The current active profile is session state controlled by the server. Activation must check `profile.owner_user_id == principal.user_id`. Foreign and nonexistent profile IDs return the same not-found response.
- Profile-scoped caches and in-memory models use `(ProfileID, model/version)` keys. A profile switch never reuses another profile's taste or queue state.
- Deleting a user/profile is initially soft deletion. Hard deletion and retention/export policy require a separate data lifecycle decision.

### Data ownership

| Shared installation data | User-owned data | Profile-owned data |
|---|---|---|
| scanned tracks and paths, audio features/embeddings, artwork, artist/album catalog, scanner state, shared worker jobs | login identities, local roles, account settings | event history, recommendation requests/impressions, taste snapshots/vectors, favorites, later items, rules, contexts, playlists, play sessions, per-profile recommendation preferences |

Recommendation feedback and listening events must never update another profile's state. Installation-wide metrics, if retained, are explicitly aggregate and must not be used as a substitute for profile ownership. Public radio shares remain bearer-token resources; playback by a share listener is not owner feedback.

## Authentication contract

- Keep an authentication-provider boundary: local compatibility login and optional OIDC are providers that produce the same internal `Principal`; handlers and domain services do not depend on Keycloak.
- OIDC uses Authorization Code + PKCE, verified issuer/audience/signature/expiry/nonce, discovery/JWKS, one-time state, and an allowlisted redirect URI. Keycloak is supported through standard OIDC configuration; there is no Keycloak-specific data model.
- Browser tokens stay server-side. Successful login creates a rotated, opaque, `HttpOnly`, `Secure` (when HTTPS), `SameSite=Lax` cookie backed by revocable server session state. State-changing cookie-authenticated requests also require CSRF/Origin protection.
- Existing `MUSIK_PASSWORD` single-user installations map to one explicitly provisioned local user and its default profile during migration. They do not create one shared user for every visitor. Existing Bearer API credentials become a separately identified service principal and are not silently treated as an interactive user.
- Multi-user mode fails closed if no provider/bootstrap administrator is configured. Registration, provider linking, role assignment and account disabling are admin-controlled operations. Do not rely on Keycloak group names for authorization unless an explicit, documented mapping is configured.

## HTTP/API contract (proposed v1)

Existing library/playback URLs remain stable. They resolve the active profile from the authenticated server session; clients cannot select a data owner by adding `user_id` or `profile_id` to ordinary payloads.

- `GET /api/auth/me` returns `{ "authenticated": true, "user": {"id", "display_name", "roles"}, "active_profile": {"id", "name"}, "profiles": [...] }` (or the existing unauthenticated result).
- `GET /api/profiles` lists only the caller's profiles.
- `POST /api/profiles` accepts `{ "name": "..." }` and returns the created profile. The service creates the owner relation from the principal.
- `PATCH /api/profiles/{profile_id}` changes that profile's name/settings only after ownership validation.
- `POST /api/profiles/{profile_id}/activate` changes the current authenticated session's active profile and returns the selected profile. It does not accept an owner ID.
- `DELETE /api/profiles/{profile_id}` soft-deletes a non-last profile owned by the caller; deleting the last profile is rejected. Default-profile reassignment is transactional.
- OIDC routes are provider-named start/callback routes. Tokens and authorization codes are never returned to browser JavaScript. Exact route spelling is finalized with the OpenAPI update before implementation.
- API error envelope stays `{ "error": string, "code": string }`; use stable codes `auth_required`, `profile_not_found`, `profile_limit`, `profile_required`, and `provider_unavailable`.

Every protected endpoint derives `UserID` and active `ProfileID` from request context. Repository methods that read or mutate personal state require a `ProfileID`; access checks and mutations happen in the same transaction where a race could change ownership.

## Persistence and migration contract

- Add normalized `users`, `external_identities`, `auth_sessions`, and `profiles` records. User/profile IDs are text UUIDs on SQLite and PostgreSQL alike. Enforce ownership with foreign keys and indexes; enforce one default profile per user with an equivalent constraint/transaction rule on both engines.
- Add `profile_id` to profile-owned rows and scope every query/update/delete by it. Shared catalog rows stay unowned. Composite ownership constraints should prevent playlist items, contexts and active sessions from referencing a foreign profile's parent.
- A versioned migration creates one local owner and one default profile, then assigns all existing personal rows to that profile exactly once. It never copies legacy history or taste to each newly created account. The old single-user login remains available until the operator enables a new provider and verifies migration.
- Each step is restartable or has a defined rollback/backup path. Migration refuses ambiguous existing ownership rather than guessing.

## Acceptance contract before implementation

1. Two users cannot list, infer, mutate or delete each other's profiles or profile data, including by changing IDs in URLs, query strings, headers or JSON bodies.
2. One user can create and switch between two profiles; favorites/history/taste/playlists and active playback state remain isolated across switches and devices.
3. Duplicate `(issuer, subject)` login resolves to the same user; same email with a different subject does not auto-link.
4. Invalid issuer, audience, signature, nonce, state, expiry, redirect URI, disabled user and revoked session all fail closed.
5. Legacy single-user migration preserves every existing row under one owner/default profile, with counts and representative taste/playback behavior reconciled before and after.
6. Share-radio playback remains read-only and never writes feedback to the share owner's profile.
7. These contract cases run on SQLite and PostgreSQL once the independent database-portability workstream is implemented.

## Decisions needed before implementation

- Provisioning: invite/allowlist only (recommended for first release), or opt-in JIT for any valid OIDC user?
- Profile limit: no limit, or a configurable per-user cap? The data model supports multiple profiles either way.
- Local password: retain as an explicit local provider for recovery, or require OIDC in multi-user mode?
- User administration: first administrator via one-time CLI/bootstrap secret (recommended), or configured OIDC groups?
- Bearer integrations: map current token to a service principal with explicit scopes (recommended), or associate each token with a user/profile?
