import json, os, subprocess, sys
V = sys.argv[1]; me = os.path.join(os.path.dirname(os.path.abspath(__file__)), "verify.py")
ok = 0; bad = []
for name in sorted(os.listdir(V)):
    d = os.path.join(V, name)
    if not os.path.isdir(d): continue
    exp = json.load(open(os.path.join(d, "expected.json")))
    cmd = [sys.executable, "-I", me]
    if exp["mode"] == "bundle": cmd += ["bundle", os.path.join(d, "bundle")]
    else: cmd += ["ledger", os.path.join(d, "ledger.jsonl"), os.path.join(d, "bundles")]
    a = os.path.join(d, "anchor.pub")
    if os.path.exists(a): cmd += ["--key", a]
    cmd += ["--json"]
    r = json.loads(subprocess.run(cmd, capture_output=True, text=True).stdout)
    if r["verdict"] == exp["verdict"] and r["findings"] == sorted(exp["findings"]): ok += 1; print("MATCH   ", name)
    else: bad.append(name); print("MISMATCH", name, r, "expected", exp["verdict"], exp["findings"])
print(ok, "match,", len(bad), "mismatch")
