| Rule | Test | Case | Result |
|---|---|---|---|
| §1 | `server.TestADR008Local` | §1 the last break-glass admin cannot be removed, demoted or disabled | PASS |
| §1 | `server.TestADR008OIDC` | §1 OIDC needs a break-glass admin with a security key | PASS |
| §1 | `server.TestADR008OIDC` | §1 code flow with PKCE, state and nonce; a session for the mapped role | PASS |
| §1 | `server.TestADR008OIDC` | §1 turning OIDC off ends the OIDC sessions at their next refresh | PASS |
| §1 | `server.TestADR008OIDC` | §1 while OIDC is on only break-glass admins sign in locally; that notifies admins | PASS |
| §2 | `server.TestADR008OIDC` | §2 IdP unreachable: the session goes on, step-up is refused with idp unreachable | PASS |
| §2 | `server.TestADR008OIDC` | §2 a role change at the IdP ends the user's sessions | PASS |
| §2 | `server.TestADR008OIDC` | §2 disabled at the IdP: the session ends at the refresh | PASS |
| §2 | `server.TestADR008OIDC` | §2 removed from the group at the IdP: access ends within 10 minutes | PASS |
| §2 | `server.TestADR008OIDC` | §2 the highest role of the user's groups; no matching group, no access | PASS |
| §3 | `server.TestADR008Local` | §3 10 single-use recovery codes, stored hashed, each use audited | PASS |
| §3 | `server.TestADR008Local` | §3 TOTP: 30 s, 6 digits, ±1 step, a used code is rejected | PASS |
| §3 | `server.TestADR008Local` | §3 an admin's reset of a user's factors: the user enrols again | PASS |
| §3 | `server.TestADR008Local` | §3 an admin's second factor is a security key only | PASS |
| §3 | `server.TestADR008Local` | §3 enrolment happens at the first sign-in and cannot be skipped | PASS |
| §3 | `server.TestADR008OIDC` | §3 an ID token without a second factor is refused | PASS |
| §4 | `server.TestADR008Local` | §4 a step-up approved in the user's browser passes the CLI's action, and only the same user's | PASS |
| §4 | `server.TestADR008Local` | §4 a step-up older than 120 s, bound to another action, or used twice fails | PASS |
| §4 | `server.TestADR008Local` | §4 every step-up action fails without a fresh factor | PASS |
| §4 | `server.TestADR008Local` | §4 the step-up challenge holds the action and its target | PASS |
| §4 | `server.TestADR008Local` | §4 weakening a policy needs a step-up, tightening does not | PASS |
| §4 | `server.TestADR008OIDC` | §4 OIDC step-up: a new sign-in at the IdP with prompt=login, max age=0, auth time within 120 s | PASS |
| §5 | `server.TestADR008Local` | §5 a password, factor or role change ends the user's other sessions | PASS |
| §5 | `server.TestADR008Local` | §5 a sign-in from a new device or address is marked and notifies admins | PASS |
| §5 | `server.TestADR008Local` | §5 absolute timeout: 8 h whatever the activity | PASS |
| §5 | `server.TestADR008Local` | §5 at most 3 sessions per user; a fourth ends the oldest | PASS |
| §5 | `server.TestADR008Local` | §5 idle timeout: background requests do not keep a session alive | PASS |
| §5 | `server.TestADR008Local` | §5 sign-out ends the session on the server | PASS |
| §6 | `server.TestADR008Local` | §6 forced change after an admin's reset and after a blocklist update | PASS |
| §6 | `server.TestADR008Local` | §6 not one of the last 5 passwords | PASS |
| §6 | `server.TestADR008Local` | §6 password rules: 15-128 characters, the blocklist, the user and instance names | PASS |
| §6 | `server.TestADR008Local` | §6 periodic change: with an expiry, a reminder 14 days before and a new password first at expiry | PASS |
| §7 | `server.TestADR008Local` | §7 10 failures in 15 min pause the account from every address and notify admins | PASS |
| §7 | `server.TestADR008Local` | §7 from the 3rd failure an increasing delay, per account and per address | PASS |
| §8 | `server.TestADR008Local` | §8 auditor accounts expire, at most 30 days ahead, renewable by an admin | PASS |
| §8 | `server.TestADR008OIDC` | §8 an OIDC-mapped auditor gets an expiry | PASS |
| §9 | `server.TestADR008Local` | §9 API tokens: admin-made, operator or auditor, at most 90 days, shown once, no step-up | PASS |
| §9 | `server.TestADR008Local` | §9 the CLI's sign-in through a browser gets a session with the same limits | PASS |
| §9 | `server.TestADR008OIDC` | §9 the CLI signs in at the IdP with the device grant (RFC 8628) | PASS |
| §10 | `server.TestADR008Local` | §10 no API call that changes state succeeds without a session or token | PASS |
| §10 | `server.TestADR008Local` | §10 sign-in, sign-out, failure, step-up, session end, enrolment, reset, token and settings changes are audited | PASS |
| - | `auth.TestBlocklist` | (whole test) | PASS |
| - | `auth.TestBreached` | (whole test) | PASS |
| - | `auth.TestCheckPassword` | (whole test) | PASS |
| - | `auth.TestRecoveryCodes` | (whole test) | PASS |
| - | `auth.TestTOTPVectors` | (whole test) | PASS |
| - | `auth.TestVerifyTOTP` | (whole test) | PASS |
| - | `server.TestADR008Local` | (whole test) | PASS |
| - | `server.TestADR008OIDC` | (whole test) | PASS |
| - | `server.TestBrowserSession` | (whole test) | PASS |
| - | `server.TestPageRoles` | (whole test) | PASS |
| - | `server.TestRoleMatrix` | (whole test) | PASS |
| - | `store.TestAPITokens` | (whole test) | PASS |
| - | `store.TestMigrationFromPreviousSchema` | (whole test) | PASS |
| - | `store.TestSigninFailures` | (whole test) | PASS |
| - | `store.TestUsersAndSessions` | (whole test) | PASS |
