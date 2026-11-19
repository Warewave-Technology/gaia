# ADR-008: Authentication, sessions and single sign-on

Status: accepted (2026-11-20). Replaces ADR-007 §8's "OIDC is later" and extends §6 (sessions) and the T20
browser-session decision. Design reference: `docs/design/v2/Login.dc.html` and `Security.dc.html`.

## Context

Gaia is the tool an operator opens during a disaster, and it can destroy data (restore over a running VM, forget
points). Sign-in must therefore be strict. But the identity provider (IdP) can itself be down, or be the very VM
being restored. So single sign-on is welcome, but it can never be the only way in.

Password rotation: NIST SP 800-63B-4 (final, July 2025) says verifiers should not force periodic changes; it asks
for length, a blocklist of breached and common passwords, and a forced change only on evidence of compromise.
PCI DSS 4.0.1 8.3.9 requires 90-day rotation only for single-factor access; the PCI SSC FAQ states it does not
apply where MFA is used. Gaia requires a second factor for every account, so rotation is an option for auditors
who still ask for it, not a default.

## Decisions

### 1. Two ways in: OIDC and local accounts

- **OIDC** (optional, off by default): authorization code flow with PKCE, confidential client, `state` and
  `nonce`, issuer discovery, JWKS cached and refreshed on unknown `kid`. Only one IdP per installation.
- **Local accounts** always exist. When OIDC is on, only users marked **break-glass** may use a local account.
  At least one break-glass admin is required at all times; the last one cannot be removed, demoted or disabled.
- A break-glass sign-in while OIDC is on notifies every admin (event `breakglass_signin`), is an audit entry and
  appears in the next daily report.

### 2. Roles from the IdP

- Roles come from a configurable claim (default `groups`) through an explicit mapping `group → role`. No matching
  group means no access; there is no default role.
- A user with groups for several roles gets the highest of them (admin > operator > auditor).
- The mapping and the user's resolved role are re-read at every **session refresh (every 10 minutes)**. A user
  removed in the IdP loses access within 10 minutes; a role change ends the user's sessions (§5).
- If the IdP is **unreachable** at a refresh, the session continues until its absolute limit, the session is marked
  `idp_unreachable`, and actions that need step-up (§4) are refused with that reason. If the IdP answers that the
  user is disabled or has no mapped group, the session ends immediately.

### 3. Second factor everywhere

- Local accounts: a second factor is **required and cannot be turned off**. Admins: WebAuthn security key only.
  Operators and auditors: WebAuthn or TOTP (RFC 6238, 30 s, 6 digits, ±1 step, a used code is rejected).
- 10 single-use recovery codes per user, stored hashed; each use is an audit entry.
- OIDC users: Gaia requires the ID token to show a second factor (`acr` or `amr`, configurable; required by
  default). A token without it is rejected with that reason.
- Enrolment happens at the first sign-in and cannot be skipped. An admin can reset a user's factors (step-up,
  audited); the user must enrol again at the next sign-in.

### 4. Step-up for destructive and security actions

These actions require a fresh second factor inside the request, every time, never remembered:
restore over a running VM; forget or delete points; user, role, factor and API-token changes; signing-key rotation;
weakening a policy (any target made looser, a test interval made longer); any change in sign-in and security
settings; adding or removing a repository or a host; evidence export.

- Local users: a WebAuthn assertion or TOTP code bound to the action (the challenge includes the action and its
  target), valid for one use within 120 s.
- OIDC users: a redirect to the IdP with `prompt=login` and `max_age=0`; the returned token must show a second
  factor and an `auth_time` within the last 120 s.
- Restore over a running VM additionally requires typing the VM's name.
- Every step-up (asked, passed, failed) is an audit entry.

### 5. Sessions

- Server-side sessions in SQLite. The browser holds an opaque 256-bit token in `__Host-gaia_session`
  (Secure, HttpOnly, SameSite=Strict, Path=/); the server stores only its SHA-256.
- **Idle timeout:** 15 min (configurable 5–60). Requests that the UI makes on its own (auto-refresh, polling) carry
  `X-Gaia-Background: 1` and **do not extend** the idle timer; only user-initiated requests do.
- **Absolute timeout:** 8 h (configurable 1–12), whatever the activity.
- At most 3 sessions per user; a fourth ends the oldest.
- Sign-out ends the session on the server. A password, factor or role change ends all other sessions of that user.
- A sign-in from a device or address not seen for that user in 30 days marks the session `new_device` and notifies
  the user's admins (event `new_device_signin`).
- `CrossOriginProtection` on every route and the CSP from T20 stay.

### 6. Passwords (local accounts)

- At least 15 characters, at most 128; no composition rules.
- Checked at set time against an embedded blocklist (common and breached passwords, built from a documented public
  source during the release build and pinned by sha256), the user name, and the instance name. An optional online
  k-anonymity breach check is off by default (Gaia must work air-gapped).
- Not one of the user's last 5 passwords. Hashing stays argon2id (ADR-007 §8).
- **Periodic change:** setting `password_max_age`: `none` (default) | `90d` | `180d`. With an expiry, users are
  reminded 14 days before and must set a new password before anything else at expiry, break-glass admins included.
- A forced change happens when an admin resets a password or when a password is found on the blocklist after an
  update of the list.

### 7. Failed attempts

- Per account and per source address: from the 3rd failure, an increasing delay; after 10 failures in 15 minutes,
  the account is paused for 15 minutes from every address and admins are notified (`account_locked`).
- This replaces the T18 limits where they differ. All failures are audit entries with the source address.

### 8. Auditor accounts expire

Auditor accounts (local or OIDC-mapped) carry an expiry date, at most 30 days ahead, renewable by an admin.

### 9. API tokens for automation and the CLI

- **CLI for people:** `gaia login` uses the same sign-in (local with second factor, or OIDC via the device
  authorization grant, RFC 8628, when the IdP supports it). It gets a session with the same limits as the browser.
- **API tokens for automation:** created by an admin, named, role `operator` or `auditor` only (never admin),
  expiry at most 90 days, shown once, stored as SHA-256, optional source-address allow-list. Tokens cannot pass
  step-up, so they cannot perform §4 actions.
- Existing 12 h CLI tokens from M2 are migrated: they stop working at the upgrade and the release notes say so.

### 10. Everything is recorded

Sign-in, sign-out, failure, lockout, step-up, session end (with its reason: idle, absolute, revoked, replaced,
role change), factor enrolment and reset, token creation and use (first use and daily count), and every settings
change go to the audit log. Settings changes are also listed in the next daily report.

## Not in this ADR

SAML; SCIM provisioning; more than one IdP; hardware-bound client certificates; risk-based sign-in. Recorded in
`docs/limitations.md` with their reason.

## Consequences

- New dependencies are justified per CLAUDE.md rule 3; expected: an OIDC client (`github.com/coreos/go-oidc/v3`,
  `golang.org/x/oauth2`) and WebAuthn (`github.com/go-webauthn/webauthn`), all pure Go.
- The lab needs an IdP: a Keycloak container on the lab network, set up by a script labelled `fixture`.
