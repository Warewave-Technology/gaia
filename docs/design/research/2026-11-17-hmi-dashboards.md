# Status displays: high-performance HMI, Few/Tufte, premium operator tools

Research for T20. The HMI handbook and ISA-18.2 / EEMUA 191 were not read directly (secondary sources); the Opto 22
paper, Few's bullet-graph spec, Linear's and Vercel's posts and the AI-tells articles were.

## Principles

- **A normal screen looks boring** (ISA-101, ASM, Hollifield/Nimmo 2008): muted backgrounds and normal elements; bright
  colour, motion and sound only for abnormal states. Operators took 25% longer to find alarms when red was also used for
  equipment status (Opto 22, citing Hollifield p.50). For Gaia: healthy VMs in gruvbox neutrals; red/yellow only where
  something is wrong, never brand or buttons.
- **Information, not data**: a value beside its range, drawn as an analog bar against it ("Fluffy the cat"). For Gaia:
  draw Proven RPO against its target.
- **Situation awareness** (Endsley): perceive, comprehend, project; the trend beside the value.
- **Display hierarchy** (ISA-101/ASM): L1 overview (KPIs, top abnormals, trends), L2 task screen, L3 detail, L4
  diagnostics; shallow navigation. For Gaia: L1 fleet strip, L2 VM table, L3 VM page, L4 evidence/test/screenshots.
- **Redundant coding**: priority by shape + colour + text (~10% of men are colour-blind); abnormal colours never reused.
- **Alarm rates** (ISA-18.2/EEMUA 191): ~1 per 10 min manageable, >10 in 10 min a flood. One root cause (sandbox down)
  is one headline, not 50 red rows.
- **Conflict to resolve**: in HP-HMI gray means normal, so UNKNOWN needs its own salient encoding (hatching, "?", word).
- **Few's bullet graph** (spec rev. 2013): label, linear scale, featured measure as a dark bar ~1/3 of the track,
  1–2 comparative ticks, 2–5 qualitative ranges (ideally 3) as intensities of one hue, darkest = worst; a
  lower-is-better variant; stacked on a shared scale.
- **Tufte**: sparklines ("data-intense, design-simple, word-sized"), small multiples on a shared scale, "Compared to
  what?". **Few's 13 mistakes**: more than one screen, too little context, too much detail, misused colour, clutter.

## Encodings for Gaia

1. Proof bullet bar: per VM, shared compressed scale; dark bar Proven RPO, lighter marker Theoretical RPO, target tick,
   three bands (within target, up to 2×, beyond). The gap between marker and bar = the unproven gap.
2. Point rug / timeline strip: a lane per row on a shared time axis, now at the right; backups thin ticks; tests taller
   marks (filled pass, × fail, hollow ? could-not-run); newest provable point outlined.
3. RTO dot against its target.
4. Proof-age sparkline: growing (going stale) vs saw-tooth (healthy cadence).
5. Status cell: shape + word + colour; UNKNOWN hatched; OK plain and quiet.
6. Reason: a short phrase in the row; the full explanation in a native `<details>` (no JavaScript).
7. L1 fleet strip: counts in words, oldest proven point, worst Proven RPO vs target, one headline per shared cause.
8. VM page: state timeline from state_changes (bands, reasons), point list, screenshots as small multiples, signature
   state per bundle.

## Premium craft

One question per screen; a table, not cards; sort by severity then proof age; tabular numbers, one duration format;
absolute and relative time, timezone once in the header; chrome recedes (Linear); one text face + one mono, few sizes,
weight for hierarchy; deliberate spacing rhythm; every state designed (empty, never tested, partial, error, stale page);
the URL is the state, rows are real links; keyboard (j/k, Enter, /) and visible focus; motion only on change; test with
long names and 50 rows; product vocabulary; readable printed and without CSS.

AI-generated tells: equal-weight card grids, stock radius and shadows, one uniform gap, Inter everywhere, decorative
gradients, a coloured left-edge stripe on panels, big icons above headings, generic copy, missing empty/error states —
"the absence of decisions".

## Anti-patterns

Alarm floods; red/green only; colour inflation (a green "OK" everywhere too); wall-of-text reasons; card grids; values
without targets; vanity charts (donuts, total TB, gauges); deep navigation; animating normal things; hiding staleness.

## Links

- https://documents.opto22.com/2061_High_Performance_HMI_white_paper.pdf
- https://www.automation.com/article/the-high-performance-hmi
- https://docs.tatsoft.com/display/FX/High+Performance+HMI+Compliance+How-to
- https://www.automation.com/article/modern-dcs-graphics-level-1-overview-displays
- https://www.chemengonline.com/alarm-management-numbers/
- https://www.perceptualedge.com/articles/misc/Bullet_Graph_Design_Spec.pdf
- https://www.edwardtufte.com/notebook/sparkline-theory-and-practice-edward-tufte/
- https://grafana.com/docs/grafana/latest/panels-visualizations/visualizations/state-timeline/
- https://linear.app/now/behind-the-latest-design-refresh · https://linear.app/now/how-we-redesigned-the-linear-ui
- https://vercel.com/design/guidelines
- https://incident.io/blog/achieving-pixel-perfect-polish
- https://smart-interface-design-patterns.com/articles/cards-vs-lists-vs-tables-vs-data-grids/
- https://www.saasui.design/blog/saas-ui-looks-ai-generated · https://world.hey.com/kostac/spot-the-slop-a-ui-designer-s-guide-to-fixing-ai-defaults-4c448c9c
