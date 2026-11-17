#!/usr/bin/env python3
"""T20 step 1, second proposal: a static mock-up of the operator UI, made from real lab records (no invented numbers).

Design direction: docs/design/t20-direction.md (research in docs/design/research/). Writes index.html (the overview)
and vm-gaia-t17-web.html (one VM) next to this file: HTML, CSS and inline SVG only (the real UI must read without
JavaScript), gruvbox, IBM Plex (vendored in fonts/).

Rows of the overview come from four lab runs, each at its own moment ("as of" under each VM's name); a real screen
shows one installation at one moment. The VM page is server B (the M2 upgrade-path VM), through its API.

Run:  python3 -I docs/mockups/t20/make.py
"""
import html
import json
import math
import os
import re
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", "..", ".."))
EV = os.path.join(ROOT, "docs", "reports", "evidence")
SERVER_TZ = timezone.utc  # server B and gaia-lab run in Etc/UTC (timedatectl, 2026-11-17)
TZ_NAME = "Etc/UTC"
WINDOW = timedelta(hours=6)  # the history strip
e = html.escape


def load(*p):
    with open(os.path.join(*p)) as f:
        return json.load(f)


def data(name):
    return load(HERE, "data", name)


def t(s):
    """An RFC 3339 time with up to 9 fraction digits."""
    s = s.replace("Z", "+00:00")
    m = re.match(r"^(.*?)(\.\d+)?([+-]\d\d:\d\d)$", s)
    frac = (m.group(2) or ".0")[1:7].ljust(6, "0")
    return datetime.fromisoformat(f"{m.group(1)}.{frac}{m.group(3)}")


def clock(d):
    return d.astimezone(SERVER_TZ).strftime("%H:%M:%S")


def hhmm(d):
    return d.astimezone(SERVER_TZ).strftime("%H:%M")


def date(d):
    return d.astimezone(SERVER_TZ).strftime("%b %-d")


def dur(s, precise=False):
    """Compact durations: 37s, 6m 50s, 3h 46m, 17h, 2d 4h."""
    if s is None:
        return None
    s = int(round(s))
    if s < 60:
        return f"{s}s"
    m, sec = divmod(s, 60)
    if m < 60:
        return f"{m}m {sec}s" if sec and (precise or m < 10) else f"{m}m"
    h, m = divmod(m, 60)
    if h < 24:
        return f"{h}h {m}m" if m else f"{h}h"
    d, h = divmod(h, 24)
    return f"{d}d {h}h" if h else f"{d}d"


# --- status vocabulary: shape + word + colour ------------------------------------------------------------------
GLYPH = {"OK": "✓", "WARNING": "▲", "CRITICAL": "◆", "UNKNOWN": "?"}
RANK = {"CRITICAL": 0, "UNKNOWN": 1, "WARNING": 2, "OK": 3}


def chip(status, size=""):
    return (f'<span class="chip s-{status.lower()} {size}"><span class="g" aria-hidden="true">{GLYPH[status]}</span>'
            f'{e(status)}</span>')


# --- the proof bar: Few's bullet graph on a shared log scale ----------------------------------------------------
LO, HI = 30.0, 7 * 86400.0  # 30 s .. 7 days


def xlog(v, w):
    v = min(max(v, LO), HI)
    return w * (math.log(v) - math.log(LO)) / (math.log(HI) - math.log(LO))


SCALE_TICKS = [(60, "1m"), (600, "10m"), (3600, "1h"), (6 * 3600, "6h"), (86400, "1d"), (7 * 86400, "7d")]


def proof_bar(proven, newest, target, w=220, h=26, cls_extra=""):
    """Bands: within target / up to 2x / beyond (darker = worse). Bar: the measure. Notch: newest backup (Theoretical
    RPO). Tick: target."""
    tx, t2 = xlog(target, w), xlog(2 * target, w)
    label = (f"{dur(proven)}" if proven is not None else "nothing proven") + f", target {dur(target)}"
    out = [f'<svg class="pb {cls_extra}" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" aria-label="{e(label)}">']
    y0, bh = 6, 14
    out.append(f'<rect class="b0" x="0" y="{y0}" width="{tx:.1f}" height="{bh}"/>')
    out.append(f'<rect class="b1" x="{tx:.1f}" y="{y0}" width="{t2 - tx:.1f}" height="{bh}"/>')
    out.append(f'<rect class="b2" x="{t2:.1f}" y="{y0}" width="{w - t2:.1f}" height="{bh}"/>')
    if proven is None:
        out.append(f'<rect class="none" x="0" y="{y0}" width="{w}" height="{bh}"/>')
    else:
        cls = "ok" if proven <= target else ("warn" if proven <= 2 * target else "crit")
        out.append(f'<rect class="pbar {cls}" x="0" y="{y0 + 4.5}" width="{max(xlog(proven, w), 2):.1f}" height="5"/>')
    if newest is not None:
        nx = xlog(newest, w)
        out.append(f'<path class="notch" d="M{nx - 4:.1f} 0.5 L{nx + 4:.1f} 0.5 L{nx:.1f} 6 Z"/>')
    out.append(f'<rect class="tgt" x="{tx - 1:.1f}" y="{y0 - 3}" width="2" height="{bh + 6}"/>')
    out.append("</svg>")
    return "".join(out)


def scale_axis(w=220):
    marks = "".join(f'<span style="left:{xlog(v, w):.1f}px">{lbl}</span>' for v, lbl in SCALE_TICKS)
    return f'<span class="axis" style="width:{w}px" aria-hidden="true">{marks}</span>'


# --- the history strip: backups and tests over the last hours ---------------------------------------------------
def strip(ev, as_of, w=250, h=34, big=False):
    """ev: (time, kind, id) with kind backup, backup_failed, test_ok, test_failed, test_unknown, damaged, return."""
    start = as_of - WINDOW
    X = lambda d: w * (d - start).total_seconds() / WINDOW.total_seconds()
    base = h - 9
    out = [f'<svg class="hs{" big" if big else ""}" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" '
           f'aria-label="Backups and restore tests, last {int(WINDOW.total_seconds() // 3600)} hours">']
    for k in range(1, 6):  # hour rules
        x = w * k / 6
        out.append(f'<line class="hr" x1="{x:.1f}" y1="2" x2="{x:.1f}" y2="{base + 4}"/>')
    out.append(f'<line class="base" x1="0" y1="{base}" x2="{w}" y2="{base}"/>')
    shown = sorted((x for x in ev if start <= x[0] <= as_of), key=lambda x: ["backup", "backup_failed", "damaged", "return",
                   "test_ok", "test_unknown", "test_failed"].index(x[1]))
    if not shown:
        out.append(f'<text class="empty" x="{w / 2:.0f}" y="{base - 8}" text-anchor="middle">no backups yet</text>')
    r = 1.25 if big else 1
    for d, kind, _ in shown:
        x = X(d)
        if kind == "backup":
            out.append(f'<line class="bk" x1="{x:.1f}" y1="{base - 6 * r}" x2="{x:.1f}" y2="{base + 3}"/>')
        elif kind == "backup_failed":
            out.append(f'<path class="xf" d="M{x - 3:.1f} {base - 9} l6 6 m0 -6 l-6 6"/>')
        elif kind == "test_ok":
            out.append(f'<circle class="tok" cx="{x:.1f}" cy="{base - 14 * r}" r="{2.8 * r:.2f}"/>')
        elif kind == "test_failed":
            s = 3.2 * r
            out.append(f'<path class="tf" d="M{x - s:.1f} {base - 14 * r - s:.1f} l{2 * s:.1f} {2 * s:.1f} m0 {-2 * s:.1f} l{-2 * s:.1f} {2 * s:.1f}"/>')
        elif kind == "test_unknown":
            out.append(f'<circle class="tun" cx="{x:.1f}" cy="{base - 14 * r}" r="{3 * r:.2f}"/>')
        elif kind == "damaged":
            out.append(f'<rect class="dmg" x="{x - 3.5:.1f}" y="{base - 3.5}" width="7" height="7"/>')
        elif kind == "return":
            out.append(f'<circle class="ret" cx="{x:.1f}" cy="{base}" r="{5.5 * r:.2f}"/>')
    out.append(f'<line class="now" x1="{w - 0.75}" y1="1" x2="{w - 0.75}" y2="{base + 4}"/>')
    out.append("</svg>")
    return "".join(out)


# --- the four rows ------------------------------------------------------------------------------------------------
def row_b():
    st = data("b-status.json")[0]
    pol = data("b-policies.json")[0]
    hist = data("b-history.json")
    pts = {p["id"]: p for p in hist["points"]}
    proven = 249
    as_of = t(pts[proven]["taken_at"]) + timedelta(seconds=st["proven_rpo_s"])
    test = [x for x in hist["tests"] if x["id"] == 125][0]
    ev = [(t(p["taken_at"]), "backup", p["id"]) for p in hist["points"]]
    for x in hist["tests"]:
        kind = {"OK": "test_ok", "CRITICAL": "test_failed"}.get(x["verdict"], "test_unknown")
        ev.append((t(x["finished_at"]), kind, x["id"]))
    for j in data("b-jobs-failed.json"):
        if j["kind"] == "backup":
            ev.append((t(j["finished_at"]), "backup_failed", j["id"]))
    ev.append((t(pts[proven]["taken_at"]), "return", proven))
    return {
        "vm": st["vm"], "host": "gaia-lab", "href": "vm-gaia-t17-web.html", "st": st, "as_of": as_of, "pol": pol, "ev": ev,
        "ret": (proven, t(pts[proven]["taken_at"]), f'test {test["id"]} at {hhmm(t(test["finished_at"]))}'),
        "short": "Proven within every target.",
        "source": "server B (gaia-t17-b), live, through its API",
    }


def row_t19():
    st = load(EV, "2026-11-17-T19", "lab", "status-after-damage.json")
    pol = load(EV, "2026-11-17-T19", "lab", "policy.json")
    # run 5's server journal (pid 305864): backups, the failed backup and the tests
    ev, done = [], {}
    pat = re.compile(r'time=(\S+) level=\w+ msg="([^"]+)" (.*)$')
    with open(os.path.join(EV, "2026-11-17-T19", "lab", "server.journal")) as f:
        for line in f:
            if "gaia-t19[305864]" not in line:
                continue
            m = pat.search(line)
            if not m:
                continue
            at, msg, rest = t(m.group(1)), m.group(2), m.group(3)
            kv = dict(re.findall(r'(\w+)=("[^"]*"|\S+)', rest))
            if msg == "backup done":
                taken = at - timedelta(seconds=float(kv["seconds"]))
                done[int(kv["point"])] = taken
                ev.append((taken, "backup", int(kv["point"])))
            elif msg == "backup failed":
                ev.append((at, "backup_failed", int(kv["job"])))
            elif msg == "restore test":
                ev.append((at, "test_ok" if kv["verdict"] == "OK" else "test_failed", int(kv["test"])))
    as_of = t("2026-11-17T18:57:23.406Z") + timedelta(seconds=st["proof_age_s"])  # test 2 ended, + proof age
    ev.append((done[1], "damaged", 1))
    ev.append((done[2], "return", 2))
    return {
        "vm": st["vm"], "host": "gaia-lab", "href": None, "st": st, "as_of": as_of, "pol": pol, "ev": ev,
        "ret": (2, done[2], "test 2 at 18:57"),
        "short": "Point 1 is damaged and quarantined. Point 2, taken after it, is proven.",
        "source": "T19 lab run, after one byte of the repository was damaged (docs/reports/evidence/2026-11-17-T19/lab)",
    }


def row_t16():
    st = load(EV, "2026-11-16-T16", "status-end.json")
    pol = load(EV, "2026-11-16-T16", "policy.json")
    ev = [(t(p["taken_at"]), "backup", p["id"]) for p in load(EV, "2026-11-16-T16", "points.json")]
    for x in load(EV, "2026-11-16-T16", "tests.json"):
        kind = {"OK": "test_ok", "CRITICAL": "test_failed"}.get(x["verdict"], "test_unknown")
        ev.append((t(x["finished_at"]), kind, x["id"]))
    finished = t("2026-11-16T16:49:17.428938Z")  # test 4
    as_of = finished + timedelta(seconds=st["proof_age_s"])
    p4 = [p for p in load(EV, "2026-11-16-T16", "points.json") if p["id"] == 4][0]
    ev.append((t(p4["taken_at"]), "return", 4))
    return {
        "vm": st["vm"], "host": "gaia-lab", "href": None, "st": st, "as_of": as_of, "pol": pol, "ev": ev,
        "ret": (4, t(p4["taken_at"]), "test 4 at 16:49"),
        "short": "The proof is older than allowed. The last two tests could not run: the sandbox is unreachable.",
        "source": "T16 lab run, with the sandbox helper stopped (docs/reports/evidence/2026-11-16-T16)",
    }


def row_t18():
    ch = [c for c in load(EV, "2026-11-17-T18", "state-changes.json") if c["entity"] == "vm_status"][0]
    pol = load(EV, "2026-11-17-T18", "policy.json")
    st = {"vm": "gaia-t18", "status": ch["to"], "changed_at": ch["at"], "theoretical_rpo_s": None, "proven_rpo_s": None,
          "proven_rto_s": None, "proof_age_s": None, "reasons": [{"status": "UNKNOWN", "rule": "no_checked_point", "text": ch["reason"]}],
          "notes": []}
    return {
        "vm": "gaia-t18", "host": "gaia-lab", "href": None, "st": st, "as_of": t(ch["at"]), "pol": pol, "ev": [],
        "ret": None,
        "short": "Just added. No backup has passed its check yet.",
        "source": "T18 lab run, the moment the VM was added (docs/reports/evidence/2026-11-17-T18)",
    }


# --- page frame ---------------------------------------------------------------------------------------------------
def page(title, body, here, as_of):
    nav = [("index.html", "Overview"), ("#", "Evidence"), ("#", "Notifications"), ("#", "Settings")]
    links = "".join(f'<a href="{h}"{" aria-current=\"page\"" if n == here else ""}>{e(n)}</a>' for h, n in nav)
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{e(title)}</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<header class="bar">
  <a class="mark" href="index.html" aria-label="Gaia overview"><svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="10" cy="10" r="7.25"/><path d="M10 2.75v14.5M2.75 10h14.5"/></svg>Gaia</a>
  <span class="inst">gaia-t17-b</span>
  <nav aria-label="Main">{links}</nav>
  <span class="clock" title="Times on these pages are the server's local time ({TZ_NAME})">{hhmm(as_of)}<span>server time, {TZ_NAME}</span></span>
  <span class="user">admin</span>
</header>
<p class="mock">Proposal 2 for T20: static, made from real lab records by <code>docs/mockups/t20/make.py</code>. Only the VM name links work.</p>
{body}
</body>
</html>
"""


LEGEND = """<div class="legend" aria-hidden="true">
  <span><svg width="26" height="10"><rect class="lg-bar" x="0" y="2.5" width="26" height="5"/></svg>proven RPO</span>
  <span><svg width="10" height="10"><path class="lg-notch" d="M1 1 L9 1 L5 7 Z"/></svg>newest backup</span>
  <span><svg width="6" height="12"><rect class="lg-tgt" x="2" y="0" width="2" height="12"/></svg>target</span>
  <span class="sep"></span>
  <span><svg width="10" height="12"><line class="lg-bk" x1="5" y1="1" x2="5" y2="11"/></svg>backup</span>
  <span><svg width="10" height="10"><circle class="lg-tok" cx="5" cy="5" r="3"/></svg>restore test passed</span>
  <span><svg width="10" height="10"><path class="lg-tf" d="M2 2 l6 6 m0 -6 l-6 6"/></svg>failed</span>
  <span><svg width="10" height="10"><circle class="lg-tun" cx="5" cy="5" r="3"/></svg>could not run</span>
  <span><svg width="14" height="14"><circle class="lg-ret" cx="7" cy="7" r="5.5"/></svg>point to return to</span>
  <span><svg width="10" height="10"><rect class="lg-dmg" x="1.5" y="1.5" width="7" height="7"/></svg>damaged point</span>
</div>"""


def overview():
    rows = sorted([row_b(), row_t19(), row_t16(), row_t18()],
                  key=lambda r: (RANK[r["st"]["status"]], -(r["st"].get("proof_age_s") or 1e12)))
    counts = {s: sum(1 for r in rows if r["st"]["status"] == s) for s in RANK}
    b = row_b()
    sysb, ledger = data("b-system.json"), data("b-ledger.json")["latest"]
    worst = max((r for r in rows if r["st"]["proof_age_s"] is not None), key=lambda r: r["st"]["proof_age_s"])
    never = [r["vm"] for r in rows if r["st"]["proven_rpo_s"] is None]
    kit = sysb["recovery_kit"]

    tally = "".join(
        f'<li class="t s-{s.lower()}{" zero" if not counts[s] else ""}"><span class="n">{counts[s]}</span>{chip(s, "sm")}</li>'
        for s in ["CRITICAL", "UNKNOWN", "WARNING", "OK"])

    trs = []
    for r in rows:
        st, pol = r["st"], r["pol"]
        s = st["status"].lower()
        name = f'<a href="{r["href"]}">{e(r["vm"])}</a>' if r["href"] else e(r["vm"])
        if r["ret"]:
            pid, taken, by = r["ret"]
            ret = (f'<span class="rt">{clock(taken)}</span><span class="rd">{date(taken)}, point {pid}</span>'
                   f'<span class="rs">proven by {e(by)}</span>')
        else:
            ret = '<span class="rt none">nothing proven</span><span class="rs">no restore test has passed</span>'
        rpo = st["proven_rpo_s"]
        rpo_txt = (f'<b>{dur(rpo)}</b><span class="of">target {dur(pol["rpo_target_s"])}</span>' if rpo is not None
                   else '<b class="none">none</b>')
        rto = st["proven_rto_s"]
        rto_txt = (f'<b>{dur(rto)}</b><span class="of">target {dur(pol["rto_target_s"])}</span>' if rto is not None
                   else '<b class="none">—</b>')
        age = st["proof_age_s"]
        age_cls = "over" if age is not None and age > pol["max_proof_age_s"] else ""
        age_txt = (f'<b class="{age_cls}">{dur(age)}</b><span class="of">max {dur(pol["max_proof_age_s"])}</span>' if age is not None
                   else '<b class="none">—</b>')
        details = "".join(f'<li>{chip(x["status"], "xs")}<span>{e(x["text"])}</span></li>' for x in st["reasons"])
        details += "".join(f'<li class="note"><span class="nl">note</span><span>{e(n)}</span></li>' for n in st.get("notes") or [])
        trs.append(f"""<tr class="r s-{s}">
  <th scope="row" class="c-vm">{chip(st['status'])}<span class="vm">{name}</span><span class="sub">{e(r['host'])}, as of {date(r['as_of'])} {clock(r['as_of'])}</span></th>
  <td class="c-ret">{ret}</td>
  <td class="c-proof">{proof_bar(rpo, st['theoretical_rpo_s'], pol['rpo_target_s'])}<span class="fig">{rpo_txt}</span></td>
  <td class="c-num">{rto_txt}</td>
  <td class="c-num">{age_txt}</td>
  <td class="c-hist">{strip(r['ev'], r['as_of'])}</td>
  <td class="c-why"><p>{e(r['short'])}</p><details><summary>Reasons</summary><ul>{details}</ul></details></td>
</tr>""")

    sources = "".join(f"<li><b>{e(r['vm'])}</b>: {e(r['source'])}, as of {date(r['as_of'])} {clock(r['as_of'])}.</li>" for r in rows)
    body = f"""<main class="ov">
<section class="fleet" aria-labelledby="q">
  <div class="q">
    <h1 id="q">Which point can I return to?</h1>
    <p>{len(rows)} protected VMs, problems first. A healthy machine stays quiet.</p>
  </div>
  <ul class="tally" aria-label="VMs by status">{tally}</ul>
  <dl class="facts">
    <div><dt>Oldest proof</dt><dd><b>{dur(worst['st']['proof_age_s'])}</b>{e(worst['vm'])}</dd></div>
    <div><dt>Never proven</dt><dd><b>{len(never)}</b>{e(', '.join(never)) or 'none'}</dd></div>
    <div><dt>Evidence ledger</dt><dd>{chip(ledger['verdict'], 'xs')}{ledger['entries']} entries, checked {hhmm(t(ledger['checked_at']))}</dd></div>
    <div><dt>Recovery kit</dt><dd>{chip('OK', 'xs')}kit {kit['generation']} is stored off the server</dd></div>
  </dl>
  <p class="alert s-warning"><span class="g" aria-hidden="true">▲</span><span><b>Nobody is told when a status changes.</b> No notification channel is enabled on this server.</span><a href="#">Add a channel</a></p>
</section>

<section class="vms" aria-label="Protected VMs">
{LEGEND}
<table class="grid">
<thead><tr>
  <th scope="col">VM</th>
  <th scope="col">Return to</th>
  <th scope="col" class="c-proof">Proven RPO against target{scale_axis()}</th>
  <th scope="col" class="c-num">Proven RTO</th>
  <th scope="col" class="c-num">Proof age</th>
  <th scope="col" class="c-hist">Last 6 hours</th>
  <th scope="col">Why</th>
</tr></thead>
<tbody>
{chr(10).join(trs)}
</tbody>
</table>
</section>

<section class="src">
  <h2>Where these rows come from</h2>
  <p>This proposal shows four statuses Gaia really computed in the lab, each at its own moment. A real overview shows one installation at one moment.</p>
  <ul>{sources}</ul>
</section>
</main>"""
    return page("Overview — Gaia", body, "Overview", b["as_of"])


# --- one VM ---------------------------------------------------------------------------------------------------------
def bullet(label, value, target, word="target", w=250):
    over = value is not None and value > target
    return f"""<div class="kb">
  <span class="kl">{label}</span>
  <span class="kv"><b class="{'over' if over else ''}">{dur(value, True) if value is not None else '—'}</b><span class="of">{word} {dur(target)}</span></span>
  {proof_bar(value, None, target, w=w, h=26, cls_extra="lg")}
</div>"""


def vm_page():
    st = data("b-status.json")[0]
    pol = data("b-policies.json")[0]
    repo = data("b-repositories.json")[0]
    host = data("b-hosts.json")[0]
    hist = data("b-history.json")
    shist = data("b-status-history.json")
    tests = data("b-tests.json")
    present = data("b-points-present.json")
    evid = {x["test_id"]: x for x in data("b-evidence.json")}
    ledger = data("b-ledger.json")["latest"]
    reports = data("b-daily-reports.json")
    ver = data("b-bundle125-verify.json")
    r = row_b()
    as_of = r["as_of"]
    pts = {p["id"]: p for p in hist["points"]}
    t125 = [x for x in tests if x["id"] == 125][0]
    p249 = pts[249]
    d = t125["detail"]
    boot = d["checks"][0]["seconds"]

    # state timeline since the VM was added: bands with a minimum width so short states stay visible
    t_first, t_end = t(shist[0]["at"]), as_of
    segs = []
    for i, c in enumerate(shist):
        a = t(c["at"])
        bnd = t(shist[i + 1]["at"]) if i + 1 < len(shist) else t_end
        segs.append((c["to"], a, bnd, c["reason"]))
    total = (t_end - t_first).total_seconds()
    band = "".join(
        f'<span class="seg s-{to.lower()}" style="flex:{max((bnd - a).total_seconds() / total, 0.012):.4f}" '
        f'title="{e(to)} {date(a)} {clock(a)}, for {dur((bnd - a).total_seconds())}"></span>' for to, a, bnd, _ in segs)
    band_axis = f'<span>{date(t_first)} {hhmm(t_first)}</span><span>{date(t_end)} {hhmm(t_end)}</span>'
    changes = []
    for i in range(len(segs) - 1, -1, -1):
        to, a, bnd, reason = segs[i]
        frm = segs[i - 1][0] if i > 0 else None
        lasted = dur((bnd - a).total_seconds())
        changes.append(f"""<li class="s-{to.lower()}"><time>{date(a)} {clock(a)}</time><span class="tr">{chip(frm, 'xs') if frm else '<span class="new">added</span>'}<span class="ar">→</span>{chip(to, 'xs')}</span><span class="for">{lasted + ' so far' if i == len(segs) - 1 else 'for ' + lasted}</span><p>{e(reason)}</p></li>""")

    big = strip(r["ev"], as_of, w=1080, h=60, big=True)
    hours = "".join(f'<span style="left:{100 * k / 6:.3f}%">{hhmm(as_of - WINDOW + timedelta(hours=k))}</span>' for k in range(0, 7))

    checks = "".join(f"""<li>{chip(c['verdict'], 'xs')}<span class="cn">{e(c['name'].replace('_', ' '))}</span><span class="ct">{dur(c['seconds'], True) if c['seconds'] else ''}</span><span class="cd">{e(c['detail'])}</span></li>""" for c in d["checks"])

    def evstate(x):
        if not x.get("bundle_id"):
            return '<span class="muted">no bundle</span>'
        en = evid.get(x["id"])
        if x["id"] == 125:
            return f'{chip("OK", "xs")}<span class="muted">verified</span>'
        if en and en["ledger_seq"] <= ledger["head_seq"]:
            return f'{chip("OK", "xs")}<span class="muted">check {hhmm(t(ledger["checked_at"]))}</span>'
        return '<span class="pending">not checked yet</span>'

    trows = "".join(f"""<tr class="s-{x['verdict'].lower()}"><td class="num">{x['id']}</td><td class="num">{x['point_id']}</td><td>{chip(x['verdict'], 'xs')}</td>
<td class="num">{dur(x['proven_rto_s']) or '—'}</td><td class="num">{date(t(x['finished_at']))} {clock(t(x['finished_at']))}</td><td>{evstate(x)}</td></tr>"""
                    + (f"""<tr class="why s-{x['verdict'].lower()}"><td></td><td colspan="5">{e(x['reason'])}</td></tr>""" if x["verdict"] != "OK" else "")
                    for x in tests)
    tested = {x["point_id"]: x["id"] for x in hist["tests"] if x["verdict"] == "OK"}
    prows = "".join(f"""<tr{' class="ret"' if p['id'] == 249 else ''}><td class="num">{p['id']}</td><td class="num">{date(t(p['taken_at']))} {clock(t(p['taken_at']))}</td><td>{e(p['kind'])}</td>
<td class="num">{p['bytes'] / 1048576:.0f} MiB</td><td>{chip('OK' if p['check_state'] == 'ok' else 'CRITICAL', 'xs')}</td>
<td>{('<span class="proved">test ' + str(tested[p['id']]) + '</span>' + (' <span class="rmark">return here</span>' if p['id'] == 249 else '')) if p['id'] in tested else '<span class="muted">not tested</span>'}</td></tr>""" for p in present)

    rep = reports[0]
    restore = d["restore_s"]
    rto_w = 100 * restore / t125["proven_rto_s"]
    rto_scale = 100 * t125["proven_rto_s"] / pol["rto_target_s"]
    body = f"""<main class="vmp">
<nav class="crumbs" aria-label="Breadcrumb"><a href="index.html">Overview</a><span aria-hidden="true">/</span><span>gaia-t17-web</span></nav>

<section class="head">
  <div class="id">
    <h1>gaia-t17-web</h1>
    {chip(st['status'], 'lg')}
    <p class="sub">{st['status']} since {date(t(st['changed_at']))} {clock(t(st['changed_at']))}. Host {e(host['name'])} ({e(host['address'])}), policy <b>{e(pol['name'])}</b>, repository <b>{e(repo['name'])}</b>.</p>
  </div>
  <div class="answer">
    <span class="lbl">You can return to</span>
    <span class="when">{clock(t(p249['taken_at']))}</span>
    <span class="what"><b>{date(t(p249['taken_at']))}, point 249.</b> Test 125 restored it and booted it to a login prompt in {dur(t125['proven_rto_s'], True)}, {dur((as_of - t(t125['finished_at'])).total_seconds())} ago.</span>
  </div>
  <div class="kbs">
    {bullet('Proven RPO', st['proven_rpo_s'], pol['rpo_target_s'])}
    {bullet('Theoretical RPO <span class="hint">age of the newest backup</span>', st['theoretical_rpo_s'], pol['rpo_target_s'])}
    {bullet('Proven RTO', st['proven_rto_s'], pol['rto_target_s'])}
    {bullet('Proof age', st['proof_age_s'], pol['max_proof_age_s'], 'max')}
  </div>
  <p class="acts"><a class="btn" href="#">Back up now</a><a class="btn" href="#">Test a restore now</a><a class="btn quiet" href="#">Export evidence</a></p>
</section>

<section class="hist">
  <h2>Last 6 hours <span class="h2s">a backup every {dur(pol['backup_interval_s'])}, a restore test every {dur(pol['test_interval_s'])}, the last {pol['keep_last']} points kept</span></h2>
  <div class="bigstrip">{big}<div class="hours" aria-hidden="true">{hours}</div></div>
  {LEGEND}
</section>

<div class="two">
<section class="test">
  <h2>Latest restore test <span class="h2s">test 125 of point 249, {date(t(t125['finished_at']))} {clock(t(t125['finished_at']))}</span></h2>
  <div class="shots">
    <figure><img src="img/b-test125-boot.png" width="720" height="400" alt="Console of the restored VM when the boot check passed: the login prompt"><figcaption>Boot check passed after {dur(boot, True)}</figcaption></figure>
    <figure><img src="img/b-test125-end.png" width="720" height="400" alt="Console of the restored VM at the end of the test: Debian GNU/Linux 12 gaia-t17-web tty1, gaia-t17-web login:"><figcaption>End of the test</figcaption></figure>
  </div>
  <div class="rto">
    <span class="lbl">Proven RTO <b>{dur(t125['proven_rto_s'], True)}</b> <span class="of">of the {dur(pol['rto_target_s'])} target</span></span>
    <span class="track"><span class="split" style="width:{rto_scale:.2f}%"><span class="rs" style="width:{rto_w:.1f}%"></span><span class="bs" style="width:{100 - rto_w:.1f}%"></span></span></span>
    <span class="keys"><span class="k rs">restore {dur(restore, True)}</span><span class="k bs">boot {dur(boot, True)}</span></span>
  </div>
  <ul class="checks">{checks}</ul>
</section>

<section class="ev">
  <h2>Evidence</h2>
  <dl>
    <div><dt>Bundle</dt><dd><code>{e(t125['bundle_id'])}</code></dd></div>
    <div><dt>Signature</dt><dd>{chip(ver['verdict'], 'xs')}manifest signed, all {ver['report']['files']} files match</dd></div>
    <div><dt>Signed by</dt><dd><code title="{e(ver['report']['key_id'])}">{e(ver['report']['key_id'][:16])}…</code></dd></div>
    <div><dt>Ledger</dt><dd>entry {evid[125]['ledger_seq']}; the check at {hhmm(t(ledger['checked_at']))} found {ledger['entries']} entries {chip(ledger['verdict'], 'xs')}</dd></div>
    <div><dt>External anchor</dt><dd>daily report {rep['id']} ({date(t(rep['generated_at']))} {hhmm(t(rep['generated_at']))}) recorded the head at entry {rep['head_seq']}</dd></div>
  </dl>
  <div class="cmd"><span>Verify offline, with the export and the mailed daily report</span><code>gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles -head gaia-daily-report-{rep['id']}.json</code></div>
</section>
</div>

<section class="sh">
  <h2>Status history <span class="h2s">since the VM was added</span></h2>
  <div class="band" aria-hidden="true">{band}</div>
  <div class="bandaxis" aria-hidden="true">{band_axis}</div>
  <ol class="changes">{''.join(changes)}</ol>
</section>

<div class="two">
<section>
  <h2>Points <span class="h2s">{len(present)} kept, {sum(1 for p in hist['points'] if p['state'] == 'pruned')} pruned by retention</span></h2>
  <table class="list"><thead><tr><th>Point</th><th>Taken</th><th>Kind</th><th class="num">Size</th><th>Check</th><th>Restore test</th></tr></thead><tbody>{prows}</tbody></table>
</section>
<section>
  <h2>Restore tests <span class="h2s">the newest 12, and those that did not pass</span></h2>
  <table class="list"><thead><tr><th>Test</th><th>Point</th><th>Result</th><th class="num">RTO</th><th>Finished</th><th>Evidence</th></tr></thead><tbody>{trows}</tbody></table>
</section>
</div>
</main>"""
    return page("gaia-t17-web — Gaia", body, "", as_of)


if __name__ == "__main__":
    for name, content in [("index.html", overview()), ("vm-gaia-t17-web.html", vm_page())]:
        with open(os.path.join(HERE, name), "w") as f:
            f.write(content)
        print("wrote", os.path.join("docs/mockups/t20", name))
