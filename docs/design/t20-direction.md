# T20 design direction: "calm until it matters"

PO decisions (2026-11-17): the first mock-up is rejected ("too simple, unusable, too complex"; it must not look like a
standard AI design); premium product; theme gruvbox; UI in English; times in the server's local time. Research:
`docs/design/research/` (gruvbox, HMI and dashboards, backup products).

## What the screens are for

One question, everywhere: **which point can I return to right now, and is that proven?** No backup product shows this
per VM on its main screen (research: backup products). It is Gaia's reason to exist, so the screens are built around it.

## Principles (each from the research)

1. **Calm when healthy, loud only when not** (high-performance HMI, ISA-101). An OK row uses the neutral gruvbox inks;
   red and yellow appear only where something is wrong and never as decoration, brand or buttons. OK is quiet, not a
   wall of green.
2. **UNKNOWN is never calm**: purple, hatched, a "?" glyph and the word (in HMI gray means normal, so UNKNOWN cannot be
   gray).
3. **Every number against its target** (HMI "information, not data"; Few's bullet graph). Proven RPO, Theoretical RPO,
   Proven RTO and proof age are drawn as bars on a shared scale with the target tick; the gap between the newest backup
   and the newest proof is visible at a glance.
4. **History as a strip, not a sentence** (Tufte: small multiples, word-sized graphics). Each row carries the last hours
   of backups and restore tests on a shared time axis: tested points, failed tests, tests that could not run, and the
   point you would return to.
5. **Short reason in the row, the full reason on demand** (native `<details>`, no JavaScript).
6. **A table, not cards; the hierarchy is shallow** (L1 fleet band → L2 VM table → L3 VM page → L4 test and evidence).
7. **Absolute time and age together**, in server time, with the time zone stated once.
8. **Status by shape, word and colour together**: ◆ CRITICAL, ▲ WARNING, ? UNKNOWN, ✓ OK.

## Tokens

Gruvbox (morhetz). Dark is the default; light follows the system. Page/surface/raised: dark bg0_h #1d2021 / bg0
#282828 / bg1 #3c3836; light bg0 #fbf1c7 / bg0_h #f9f5d7 / bg0_s #f2e5bc. Text: fg1 body, fg0 headings, fg4 (dark) /
fg3 (light) secondary. Status: CRITICAL red (#fb4934 dark, #9d0006 light), WARNING yellow (#fabd2f, AA variant #976311),
UNKNOWN purple (#d3869b, #8f3f71), OK green only in the glyph (#b8bb26, #74700d). Blue (#83a598, #076678) for links and
focus; aqua (#8ec07c, #427b58) for "proven" marks on the history strip. Orange is not used (too close to red). Status
chips Zed-style: tinted fill and border, glyph and word in the status colour.

Type: IBM Plex Sans 400/500/600 for everything, IBM Plex Mono 400 for IDs and hashes (vendored woff2, SIL OFL 1.1).
Sizes 12.5 / 14 / 16 / 21 / 30; weight, not size, carries hierarchy; tabular numbers everywhere.

## Avoided on purpose (AI-design tells and ops anti-patterns)

Card grids; coloured left-edge stripes; one uniform gap; decorative gradients and shadows; big icons over headings;
wall-of-text reasons; values without targets; vanity charts; colour inflation (a green OK everywhere).
