#!/usr/bin/env python3
"""T20 step 1: a static mock-up of the operator UI, made from real lab records (no invented numbers).

Writes index.html (the status screen) and vm-gaia-t17-web.html (one VM's page) next to this file. Static HTML and
CSS only: the real UI (Go html/template + vendored htmx) must read without JavaScript too.

Rows of the status screen come from four lab runs, each as it was at its own moment (see "Where these rows come
from" at the bottom of index.html). The VM page is server B (the M2 upgrade-path VM), through its API.

Run:  python3 -I docs/mockups/t20/make.py
"""
import html
import json
import os
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", "..", ".."))
EV = os.path.join(ROOT, "docs", "reports", "evidence")


def load(*p):
    with open(os.path.join(*p)) as f:
        return json.load(f)


def data(name):
    return load(HERE, "data", name)


def t(s):
    """Parses an RFC 3339 time with up to 9 fraction digits."""
    s = s.replace("Z", "+00:00")
    if "." in s:
        head, rest = s.split(".", 1)
        frac, tz = rest[:rest.index("+")], rest[rest.index("+"):]
        s = head + "." + (frac + "000000")[:6] + tz
    return datetime.fromisoformat(s)


def hm(d):
    return d.astimezone(timezone.utc).strftime("%H:%M:%S")


def day(d):
    return d.astimezone(timezone.utc).strftime("%Y-%m-%d")


def dur(s):
    if s is None:
        return None
    s = int(round(s))
    if s < 60:
        return f"{s} s"
    m, s = divmod(s, 60)
    if m < 60:
        return f"{m} min {s} s" if s else f"{m} min"
    h, m = divmod(m, 60)
    return f"{h} h {m} min" if m else f"{h} h"


e = html.escape

GLYPH = {"OK": "✓", "WARNING": "!", "CRITICAL": "✕", "UNKNOWN": "?"}
RANK = {"CRITICAL": 0, "UNKNOWN": 1, "WARNING": 2, "OK": 3}


def badge(status, small=False):
    cls = "badge s-" + status.lower() + (" small" if small else "")
    return f'<span class="{cls}"><span class="glyph" aria-hidden="true">{GLYPH.get(status, "?")}</span>{e(status)}</span>'


def page(title, body, here):
    nav = [("index.html", "Status"), ("#", "Evidence export"), ("#", "Notifications"), ("#", "Settings")]
    links = "".join(
        f'<a href="{h}"{" aria-current=\"page\"" if n == here else ""}>{e(n)}</a>' for h, n in nav)
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{e(title)}</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<header class="top">
  <a class="brand" href="index.html">Gaia</a>
  <span class="install">gaia-t17-b</span>
  <nav aria-label="Main">{links}</nav>
  <span class="who">admin <span class="role">admin</span></span>
</header>
<p class="mock">Mock-up for T20, step 1. Static, made from real lab records by <code>docs/mockups/t20/make.py</code>;
buttons and links other than the VM name do nothing yet.</p>
<main>
{body}
</main>
</body>
</html>
"""


# ---------------------------------------------------------------------------------------------------------------
# The four rows of the status screen. Each: the status as the server computed it, and when ("as of").

def row_b():
    st = data("b-status.json")[0]
    pts = {p["id"]: p for p in data("b-points-present.json")}
    proven = 249  # the reason names it: "point 249 is proven by test 125"
    as_of = t(pts[proven]["taken_at"]) + timedelta(seconds=st["proven_rpo_s"])
    test = [x for x in data("b-tests.json") if x["id"] == 125][0]
    return {
        "vm": st["vm"], "href": "vm-gaia-t17-web.html", "st": st, "as_of": as_of,
        "answer": f'Point {proven}, taken {hm(t(pts[proven]["taken_at"]))}. '
                  f'Restore tested at {hm(t(test["finished_at"]))}: it booted to a login prompt in {dur(st["proven_rto_s"])}.',
        "source": "server B (gaia-t17-b), live, through its API",
    }


def row_t19():
    st = load(EV, "2026-11-17-T19", "lab", "status-after-damage.json")
    # test 2 finished at 18:57:23.406 (server.journal); the status was read proof_age_s after it.
    as_of = t("2026-11-17T18:57:23.406323Z") + timedelta(seconds=st["proof_age_s"])
    taken = as_of - timedelta(seconds=st["proven_rpo_s"])
    return {
        "vm": st["vm"], "href": None, "st": st, "as_of": as_of,
        "answer": f"Point 2, taken {hm(taken)}, the full backup after the damage. "
                  f"Restore tested at 18:57:23: it booted in {dur(st['proven_rto_s'])}.",
        "source": "T19 lab run, after one byte of the repository was damaged and quarantined "
                  "(docs/reports/evidence/2026-11-17-T19/lab/status-after-damage.json)",
    }


def row_t16():
    st = load(EV, "2026-11-16-T16", "status-end.json")
    finished = t("2026-11-16T16:49:17.428938Z")  # test 4 (server.journal)
    as_of = finished + timedelta(seconds=st["proof_age_s"])
    taken = as_of - timedelta(seconds=st["proven_rpo_s"])
    return {
        "vm": st["vm"], "href": None, "st": st, "as_of": as_of,
        "answer": f"Point 4, taken {hm(taken)}. Its restore test passed at {hm(finished)}, "
                  f"but that proof is now older than the policy allows.",
        "source": "T16 lab run, with the sandbox helper stopped (docs/reports/evidence/2026-11-16-T16/status-end.json)",
    }


def row_t18():
    ch = [c for c in load(EV, "2026-11-17-T18", "state-changes.json") if c["entity"] == "vm_status"][0]
    st = {"vm": "gaia-t18", "status": ch["to"], "changed_at": ch["at"], "theoretical_rpo_s": None, "proven_rpo_s": None,
          "proven_rto_s": None, "proof_age_s": None, "reasons": [{"status": "UNKNOWN", "rule": "no_checked_point", "text": ch["reason"]}],
          "notes": []}
    return {
        "vm": "gaia-t18", "href": None, "st": st, "as_of": t(ch["at"]),
        "answer": "Nothing proven. No backup of this VM has passed its check yet, so there is no point to return to.",
        "source": "T18 lab run, the moment the VM was added (docs/reports/evidence/2026-11-17-T18/state-changes.json)",
    }


def figure(value, target=None, worse=False):
    if value is None:
        return '<span class="none">none</span>'
    out = f'<span class="num{" over" if worse else ""}">{e(dur(value))}</span>'
    if target:
        out += f'<span class="target">target {e(dur(target))}</span>'
    return out


def status_page():
    rows = [row_b(), row_t19(), row_t16(), row_t18()]
    rows.sort(key=lambda r: RANK[r["st"]["status"]])
    pol = data("b-policies.json")[0]
    counts = {}
    for r in rows:
        counts[r["st"]["status"]] = counts.get(r["st"]["status"], 0) + 1
    summary = ", ".join(f'{counts[s]} {s}' for s in ["CRITICAL", "UNKNOWN", "WARNING", "OK"] if s in counts)
    sysb = data("b-system.json")
    ledger = data("b-ledger.json")["latest"]
    trs = []
    for r in rows:
        st = r["st"]
        reasons = st["reasons"]
        why = f'<p class="why-main">{e(reasons[0]["text"])}</p>'
        for x in reasons[1:]:
            why += f'<p class="why-also">Also: {e(x["text"])}</p>'
        for n in st.get("notes") or []:
            why += f'<p class="why-note">Note: {e(n)}</p>'
        name = f'<a href="{r["href"]}">{e(r["vm"])}</a>' if r["href"] else e(r["vm"])
        trs.append(f"""<tr class="row s-{st['status'].lower()}">
  <td class="c-status">{badge(st['status'])}<span class="since">since {hm(t(st['changed_at']))}</span></td>
  <td class="c-vm"><span class="vmname">{name}</span><span class="asof">as of {day(r['as_of'])} {hm(r['as_of'])} UTC</span></td>
  <td class="c-answer"><p class="answer-line">{e(r['answer'])}</p><div class="why"><span class="why-label">Why {e(st['status'])}:</span>{why}</div></td>
  <td class="c-fig" data-label="Proven RPO">{figure(st['proven_rpo_s'])}</td>
  <td class="c-fig" data-label="Proven RTO">{figure(st['proven_rto_s'])}</td>
  <td class="c-fig" data-label="Proof age">{figure(st['proof_age_s'])}</td>
  <td class="c-fig" data-label="Theoretical RPO">{figure(st['theoretical_rpo_s'])}</td>
</tr>""")
    sources = "".join(f"<li><strong>{e(r['vm'])}</strong>: {e(r['source'])}; as of {day(r['as_of'])} {hm(r['as_of'])} UTC.</li>" for r in rows)
    notif = sysb["notifications"]
    kit = sysb["recovery_kit"]
    install = f"""<ul class="install-state">
  <li class="i-warning">{badge(notif['state'], True)} <span>Notifications: {e(notif['text'])}.</span> <a href="#">Add a channel</a></li>
  <li>{badge(ledger['verdict'], True)} <span>Evidence ledger: {ledger['entries']} entries, checked {hm(t(ledger['checked_at']))} UTC with the daily report.</span></li>
  <li>{badge('OK', True)} <span>Recovery kit {kit['generation']} is saved off the server.</span></li>
</ul>"""
    body = f"""<section class="hero">
  <h1>Which point can I return to?</h1>
  <p class="lede">{len(rows)} VMs: {summary}. Worst first.</p>
</section>
{install}
<table class="status">
<caption class="visually-hidden">Every VM's status, worst first</caption>
<thead><tr>
  <th scope="col">Status</th><th scope="col">VM</th><th scope="col">You can return to, and why the status</th>
  <th scope="col" class="c-fig">Proven RPO</th><th scope="col" class="c-fig">Proven RTO</th><th scope="col" class="c-fig">Proof age</th>
  <th scope="col" class="c-fig">Theoretical RPO</th>
</tr></thead>
<tbody>
{chr(10).join(trs)}
</tbody>
</table>
<section class="legend">
  <h2>What the statuses mean</h2>
  <dl>
    <div>{badge('OK', True)}<dd>The newest proven point is within the RPO target, its restore took less than the RTO target, and the proof is fresh.</dd></div>
    <div>{badge('WARNING', True)}<dd>Backups are on time, but the proof misses a target: too old, too slow, or no recent passing test.</dd></div>
    <div>{badge('CRITICAL', True)}<dd>Backups are late, the latest restore test failed, or a point that still exists is damaged.</dd></div>
    <div>{badge('UNKNOWN', True)}<dd>No restore test has ever passed, or something needed to judge is missing. Never read it as healthy.</dd></div>
  </dl>
</section>
<section class="sources">
  <h2>Where these rows come from (mock-up only)</h2>
  <p>Each row is a real status the server computed, at its own moment. A real screen shows one installation, all at the same moment.</p>
  <ul>{sources}</ul>
</section>"""
    return page("Status · Gaia", body, "Status")


# ---------------------------------------------------------------------------------------------------------------
# One VM's page: server B's gaia-t17-web.

def vm_page():
    st = data("b-status.json")[0]
    pol = data("b-policies.json")[0]
    repo = data("b-repositories.json")[0]
    host = data("b-hosts.json")[0]
    pts = data("b-points-present.json")
    pcount = data("b-points-count.json")
    tests = data("b-tests.json")
    hist = data("b-status-history.json")
    ev = {x["test_id"]: x for x in data("b-evidence.json")}
    ledger = data("b-ledger.json")["latest"]
    ver = data("b-bundle125-verify.json")
    byid = {p["id"]: p for p in pts}
    t125 = [x for x in tests if x["id"] == 125][0]
    p249 = byid[249]
    as_of = t(p249["taken_at"]) + timedelta(seconds=st["proven_rpo_s"])

    # status history, newest first, with how long each status lasted
    items = []
    for i, c in enumerate(hist):
        end = t(hist[i + 1]["at"]) if i + 1 < len(hist) else as_of
        lasted = dur((end - t(c["at"])).total_seconds())
        frm = badge(c["from"], True) if c["from"] else '<span class="new">new VM</span>'
        items.append(f"""<li class="s-{c['to'].lower()}">
  <time datetime="{e(c['at'])}">{day(t(c['at']))} {hm(t(c['at']))}</time>
  <span class="change">{frm}<span class="arrow" aria-label="to">→</span>{badge(c['to'], True)}</span>
  <span class="lasted">{'for ' + lasted if i + 1 < len(hist) else 'since then (' + lasted + ')'}</span>
  <p>{e(c['reason'])}</p>
</li>""")
    timeline = "\n".join(reversed(items))

    checks = "".join(f"""<tr><td>{e(c['name'])}</td><td>{badge(c['verdict'], True)}</td>
<td class="num">{e(dur(c['seconds']) if c['seconds'] else '—')}</td><td>{e(c['detail'])}</td></tr>""" for c in t125["detail"]["checks"])

    def evstate(test):
        x = ev.get(test["id"])
        if not test.get("bundle_id"):
            return '<span class="none">no bundle (the test did not run)</span>'
        if test["id"] == 125:
            return f'{badge("OK", True)} signature verified when this page was made'
        if x and x["ledger_seq"] <= ledger["head_seq"]:
            return f'{badge("OK", True)} in the ledger check of {hm(t(ledger["checked_at"]))}'
        return f'<span class="pending">sealed (ledger entry {x["ledger_seq"] if x else "?"}), not checked since; next check with the 07:00 report</span>'

    trows = []
    for x in tests:
        p = x["point_id"]
        trows.append(f"""<tr class="s-{x['verdict'].lower()}"><td class="num">{x['id']}</td><td class="num">{p}</td>
<td>{badge(x['verdict'], True)}</td><td class="num">{e(dur(x['proven_rto_s']) or '—')}</td>
<td>{day(t(x['finished_at']))} {hm(t(x['finished_at']))}</td><td>{evstate(x)}</td><td class="reason">{e(x['reason'])}</td></tr>""")

    tested = {x["point_id"]: x["id"] for x in tests if x["verdict"] == "OK"}
    prows = "".join(f"""<tr><td class="num">{p['id']}</td><td>{day(t(p['taken_at']))} {hm(t(p['taken_at']))}</td><td>{e(p['kind'])}</td>
<td class="num">{p['bytes'] / 1048576:.0f} MiB</td><td>{badge('OK' if p['check_state'] == 'ok' else 'CRITICAL', True)}</td>
<td>{('test ' + str(tested[p['id']])) if p['id'] in tested else '<span class="none">not tested</span>'}</td></tr>""" for p in pts)

    body = f"""<nav class="crumbs" aria-label="Breadcrumb"><a href="index.html">Status</a> / gaia-t17-web</nav>
<section class="vmhead s-{st['status'].lower()}">
  <div class="title"><h1>gaia-t17-web</h1>{badge(st['status'])}<span class="since">since {day(t(st['changed_at']))} {hm(t(st['changed_at']))} UTC</span></div>
  <p class="answer">You can return to <strong>point 249</strong>, taken {hm(t(p249['taken_at']))} UTC.
  Its restore was tested at {hm(t(t125['finished_at']))}: restored in {dur(t125['detail']['restore_s'])}, booted to a login prompt in
  {dur(t125['detail']['checks'][0]['seconds'])}, {dur(t125['proven_rto_s'])} in all.</p>
  <dl class="figs">
    <div><dt>Proven RPO</dt><dd>{figure(st['proven_rpo_s'], pol['rpo_target_s'])}</dd></div>
    <div><dt>Proven RTO</dt><dd>{figure(st['proven_rto_s'], pol['rto_target_s'])}</dd></div>
    <div><dt>Proof age</dt><dd>{figure(st['proof_age_s'], pol['max_proof_age_s'])}</dd></div>
    <div><dt>Theoretical RPO</dt><dd>{figure(st['theoretical_rpo_s'], pol['rpo_target_s'])}</dd></div>
  </dl>
  <p class="meta">Policy <strong>{e(pol['name'])}</strong>: back up every {dur(pol['backup_interval_s'])}, test a restore every
  {dur(pol['test_interval_s'])}, keep the last {pol['keep_last']} points. Repository <strong>{e(repo['name'])}</strong>
  ({e(repo['kind'])}, <code>{e(repo['location'])}</code>). Host <strong>{e(host['name'])}</strong> ({e(host['address'])}).
  As of {day(as_of)} {hm(as_of)} UTC.</p>
  <p class="actions"><a class="button" href="#">Back up now</a> <a class="button" href="#">Test a restore now</a>
  <a class="button quiet" href="#">Export evidence for an auditor</a></p>
</section>

<section>
  <h2>Status history</h2>
  <ol class="timeline">
{timeline}
  </ol>
</section>

<section>
  <h2>Latest restore test: test 125 of point 249</h2>
  <div class="test">
    <figure><img src="img/b-test125-boot.png" width="720" height="400" alt="Console of the restored VM when the boot check passed: the login prompt"><figcaption>When the boot check passed (11 s)</figcaption></figure>
    <figure><img src="img/b-test125-end.png" width="720" height="400" alt="Console of the restored VM: Debian GNU/Linux 12 gaia-t17-web tty1, gaia-t17-web login:"><figcaption>At the end of the test</figcaption></figure>
  </div>
  <table class="checks"><caption class="visually-hidden">Checks of test 125</caption>
    <thead><tr><th scope="col">Check</th><th scope="col">Result</th><th scope="col">Took</th><th scope="col">What was seen</th></tr></thead>
    <tbody>{checks}</tbody>
  </table>
  <p class="evidence">Evidence bundle <code>{e(t125['bundle_id'])}</code>, ledger entry {ev[125]['ledger_seq']}:
  {badge(ver['verdict'], True)} manifest signed by evidence key <code title="{e(ver['report']['key_id'])}">{e(ver['report']['key_id'][:16])}…</code>,
  all {ver['report']['files']} files match.</p>
</section>

<section>
  <h2>Restore tests</h2>
  <p class="intro">The newest 12, and earlier ones that did not pass. A test that could not run proves nothing and does not count.</p>
  <table class="tests">
    <thead><tr><th scope="col">Test</th><th scope="col">Point</th><th scope="col">Result</th><th scope="col">Proven RTO</th>
    <th scope="col">Finished (UTC)</th><th scope="col">Evidence</th><th scope="col">Detail</th></tr></thead>
    <tbody>{''.join(trows)}</tbody>
  </table>
</section>

<section>
  <h2>Points</h2>
  <p class="intro">The {len(pts)} points kept now. {pcount['pruned']} earlier points were removed by retention; their records stay.</p>
  <table class="points">
    <thead><tr><th scope="col">Point</th><th scope="col">Taken (UTC)</th><th scope="col">Kind</th><th scope="col">Size</th>
    <th scope="col">Post-backup check</th><th scope="col">Restore tested</th></tr></thead>
    <tbody>{prows}</tbody>
  </table>
</section>"""
    return page("gaia-t17-web · Gaia", body, "")


if __name__ == "__main__":
    for name, content in [("index.html", status_page()), ("vm-gaia-t17-web.html", vm_page())]:
        with open(os.path.join(HERE, name), "w") as f:
            f.write(content)
        print("wrote", os.path.join("docs/mockups/t20", name))
