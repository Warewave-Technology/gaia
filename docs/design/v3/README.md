# UI v3 — simplification rules (approved by the PO, 2026-11-22)

Live prototype: https://claude.ai/artifact/PE5U97rYdsj38DLFB8VZci (artboards "Overview v3" and "VM page v3").
`Main3.dc.html` and `VM3.dc.html` here are those screens, to read as a specification like `docs/design/v2/`.
v2 stays the reference for everything v3 does not change; where they differ, v3 wins.

PO's words after the T27 step-1 screens (2026-11-22): *"Ekranlar çok güzel ama çok karmaşık duruyorlar. Özellikle
hosta girdiğimde history satırında şu dikine beyaz çizgi nedir anlayamadım. Tablolar data-table olmalı, çok aşağı
gidiyorlar çünkü."* And on v3: *"Yön doğru, güzel oldu."*

## The rules (ADR-007 C15, C16)

1. **Every list is a data-table** (`ui/DataTable`): fixed height, sticky header, paged on the server
   ("1–25 of 4,102", 25 / 50 / 100 rows), one line per row (≈ 40 px), long text cut with `…` and shown whole on
   hover or in an expanding row. Filters sit above the table as chips; the URL keeps them. No list grows the page.
   This applies to: overview causes, coverage and VMs; VM points and status history; Activity; every Inventory
   table; Evidence entries; Capacity demand, expiring proofs and queue; Migration VMs; audit log; sessions.
2. **One screen answers one question.** What answers it is above the fold at 1440 × 900. Everything else goes into
   tabs or expanding rows, not further down the page. Overview: header + tabs *Needs attention · Coverage · VMs*.
   VM page: header, answer band, history, then tabs *Latest test · Points · Status history · Evidence*.
3. **Sentences, not formulas.** Every reason shown to a person is built from its reason code and parameters
   (ADR-007 C3) as a sentence an operator would say: "No login prompt on the serial console within 5 min", not
   `no match for "(?m)login: *$"`; "A restore test passed", not `proven RPO 8m54s <= 4h0m0s`. Raw text (probe
   output, error strings, paths) is under a "Technical details" disclosure, never the first thing seen.
   A cause row describes the cause, not one sample VM.
4. **One format for time and numbers** (one formatter, used everywhere):
   - durations: `52 s` under 2 min; `21 min`; `4 h 22 min`; `4 d 10 h`. Never `4h0m0s` or seconds above 2 min.
   - targets the same way: `target 4 h`.
   - times in server time: `14:05` today, `yesterday 22:51`, `Nov 19 10:43` older; never ISO strings on screen.
5. **Charts explain themselves.** Lanes are labelled where they are drawn; at most four legend items; no usage
   instructions ("short mark: backup…"). Selection is shown by a ring on the mark and a caption, never by a box
   around several marks.
6. **History strip (VM page):** two labelled lanes, *Restore tests* (dots: passed / failed / could not run) and
   *Backups* (ticks). The proven point is a labelled flag above (`▼ return here · today 04:51`). The span from it to
   *now* is shaded: what you would lose; red tint when it exceeds the RPO target, which is a dashed line labelled
   *RPO target*. Marks closer than 6 px merge into one with a count.
7. **Status words stay honest** (rule 4 of CLAUDE.md): a headline or a count never treats `UNKNOWN` as fine. Example:
   Hosts says "2 unreachable · 98 not trusted yet · 0 OK", not "2 of 100 unreachable".
8. **Job and point vocabulary** (C16): job states never use status marks; untested points are a neutral
   `BACKED UP · not tested`.

## Changes per screen (from the T27 step-1 review)

| Screen | Change |
|---|---|
| Overview | v3 layout (header, tally as filters, three tabs). Status tally on one line. Cause rows: title + short summary on one line, row expands to the long sentence, "What to do" and "Show these N VMs". Ledger fact follows C14. |
| VM page | v3 layout; four metrics in one band; new history strip (rule 6); test steps as sentences with raw output folded; points and status history as data-tables. |
| Hosts | Headline per rule 7; `host_key_untrusted` hosts grouped; "Read host key" button on one line; data-table. |
| Activity | Job vocabulary (C16); queued jobs folded by kind with counts, each showing queue position and estimated start; data-table. |
| Evidence | Headline rewritten as one sentence, no button inside text; "Check now" next to the ledger fact; entries as data-table. |
| Sign-in and security | Step-up list as action names ("Restore over a running VM", "Forget a point", …); routes only under details. |
| Proof capacity | Lists as data-tables; while slots are unknown, the model starts from the configured slots, not 0. |
| All | Duration/time formatter (rule 4); sentences from codes (rule 3). |
