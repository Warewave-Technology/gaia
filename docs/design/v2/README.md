# UI v2 — design reference (approved by the PO, 2026-11-20)

Live prototype (clickable, the PO's reference): https://claude.ai/artifact/PE5U97rYdsj38DLFB8VZci

The `.dc.html` files here are the prototype's screens. Each is ordinary HTML with inline styles inside `<x-dc>`,
plus a small `class Component` script that holds the sample data and the interactions. Read them as a
specification of layout, wording, states and behaviour; they do not run outside the design canvas, and none of
their code is meant to be copied into `web/`. Build the screens with the existing React components and tokens
(`web/src/styles/tokens.css`, `web/src/app/ui/`).

**All numbers in the prototype are sample data.** Every number on a real screen comes from the API. Where a screen
shows something the API cannot know, leave it out and list it in the report.

## Screens

| File | Screen | Key ideas |
|---|---|---|
| `Main.dc.html` | Overview | One question first: *can I return right now?* (count of VMs that can, of all). Status tally. Side facts: proof capacity, ledger, recovery kit, running now. **Needs attention grouped by cause** (reason code + scope), expandable to VMs. **Coverage by host / policy / tag** as stacked status bars, worst first, healthy groups collapsed. VM table filtered to "needs attention" by default, sorted worst first, then oldest proof. |
| `VM.dc.html` | VM page | Breadcrumb keeps the group you came from. "If it breaks now, you return to" block with the proving test. "Why it is critical" names the same cause on other VMs. Four bullets (Proven RPO, Theoretical RPO, Proven RTO, proof age). History strip (24 h / 7 d) with a selectable point. Latest test: screenshots, steps with durations, a suggested next action. Points table with proof per point. |
| `Capacity.dc.html` | Proof capacity | *Can the sandbox keep every proof fresh?* Demand vs capacity in slot-hours, by OS class; slot slider and test-interval what-if (models only). Demand by policy. Proofs expiring in the next 24 h. Test queue with order reasons and estimated starts. |
| `Activity.dc.html` | Activity | Totals (running, queued, last hour, data read). Filters by kind and state. One table of jobs with progress, duration and who started it. |
| `Migration.dc.html` | Migration check | Source and its read-only nature stated. Tally of readiness. VM list; selecting one shows the verdict sentence, the four steps (read, convert, boot test, checks) and the findings with what to do. |
| `Evidence.dc.html` | Evidence (auditor) | *Can this be proven to someone who does not trust Gaia?* Ledger chain with anchors, key, format. Export with period and scope. "What the period shows" as controls with results. Latest entries. |
| `Inventory.dc.html` | Inventory | Side menu: hosts, policies, repositories, users, sign-in and security, audit log. Each a table with status where it applies. |
| `Security.dc.html` | Sign-in and security | ADR-008 as settings: OIDC (issuer, client, group → role, second factor), local accounts and break-glass, password rules, periodic change, failed attempts, auditor expiry, sessions, cookies, step-up list; signed-in sessions with revoke. Fixed settings are labelled and have no control. |
| `Login.dc.html` | Sign-in flows | States: sign in (SSO first, local below with the reason it exists), second factor, password expired, step-up dialog for a destructive action, idle sign-out, locked. |

`canvas.json` is the canvas layout (not needed for the build).

## Language rules kept from T20

- Status is always shape + word + colour: `◆ CRITICAL`, `? UNKNOWN` (hatched purple), `▲ WARNING`, `✓ OK`.
- Healthy things stay quiet; the eye goes to what needs action.
- Tables, not cards. Numbers are tabular, units written out (`9 h 41 min`), targets next to values.
- Every screen opens with the question it answers, in small caps, and the answer as the headline.
- Server time and time zone in the header.

## Differences from the prototype that are already decided

- The prototype's `sandbox-01` in Inventory is the local sandbox of the Gaia server in M3 (remote sandbox hosts are
  backlog, ADR-007 C1).
- Step-up for OIDC users is a re-sign-in at the IdP (ADR-008 §4), as the Security screen now says.
