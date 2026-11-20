#!/usr/bin/env python3
# fixture: the T25 gate (d) spreadsheet. It writes the proof capacity's inputs, as Gaia reported them
# (GET /api/v1/capacity/inputs, one JSON object a line), into a workbook that computes the capacity again with
# spreadsheet formulas from those inputs alone (ADR-007 C4: the median of a VM's last 10 tests, else its OS class's
# median, else a default; demand = 86,400 / test interval x expected duration; capacity = slots x 86,400), next to
# Gaia's own answer (POST /api/v1/capacity/what-if) and the difference. Not part of the product.
#
#   capacity_sheet.py INPUTS.jsonl WHATIF.json OUT.xlsx
import json
import sys

from openpyxl import Workbook
from openpyxl.comments import Comment
from openpyxl.styles import Font, PatternFill
from openpyxl.utils import get_column_letter

inputs_path, whatif_path, out = sys.argv[1:4]
rows = [json.loads(l) for l in open(inputs_path) if l.strip()]
gaia = json.load(open(whatif_path))
DEFAULTS = {"linux": 180.0, "windows": 600.0}  # internal/capacity DefaultLinuxS, DefaultWindowsS

F = "Arial"
plain, bold = Font(name=F, size=10), Font(name=F, size=10, bold=True)
blue = Font(name=F, size=10, color="0000FF")
title = Font(name=F, size=12, bold=True)
key = PatternFill("solid", start_color="FFFF00")

wb = Workbook()
summary = wb.active
summary.title = "Summary"
inp = wb.create_sheet("Inputs")
cls = wb.create_sheet("Classes")

# --- Inputs: one row per VM ------------------------------------------------------------------------------------
heads = ["vm_id", "vm", "policy", "os_class", "test_interval_s"] + [f"d{i}" for i in range(1, 11)] + [
    "own_median_s", "linux_own_s", "windows_own_s", "expected_s", "tests_per_day", "demand_slot_s", "gaia_expected_s", "expected_diff_s"]
for c, h in enumerate(heads, 1):
    inp.cell(1, c, h).font = bold
n = len(rows)
last = n + 1
for i, r in enumerate(rows, 2):
    vals = [r["vm_id"], r["vm"], r["policy"], r["os_class"], r["test_interval_s"]]
    ds = r["durations_s"][:10]
    vals += ds + [None] * (10 - len(ds))
    for c, v in enumerate(vals, 1):
        if v is not None:
            inp.cell(i, c, v).font = blue  # inputs, as Gaia reported them
    f = {
        "P": f'=IF(COUNT(F{i}:O{i})=0,"",MEDIAN(F{i}:O{i}))',
        "Q": f'=IF(AND(D{i}="linux",P{i}<>""),P{i},"")',
        "R": f'=IF(AND(D{i}="windows",P{i}<>""),P{i},"")',
        "S": f'=IF(P{i}<>"",P{i},IF(D{i}="windows",Classes!$B$3,Classes!$B$2))',
        "T": f"=86400/E{i}",
        "U": f"=T{i}*S{i}",
        "W": f"=S{i}-V{i}",
    }
    for col, formula in f.items():
        inp[f"{col}{i}"] = formula
        inp[f"{col}{i}"].font = plain
    inp[f"V{i}"] = r["expected_s"]
    inp[f"V{i}"].font = blue
for col in "ABCDE":
    inp.column_dimensions[col].width = 16
inp.freeze_panes = "F2"
inp["P1"].comment = Comment("The median of the VM's last 10 tests that ran (d1 newest). Empty: it has none.", "T25")
inp["S1"].comment = Comment("Its own median, else its OS class's median (Classes).", "T25")
inp["V1"].comment = Comment("What Gaia reported for this VM (GET /api/v1/capacity/inputs, expected_s).", "T25")

# --- Classes: the class medians ----------------------------------------------------------------------------------
for c, h in enumerate(["os_class", "median_s", "default_s", "vms_with_tests", "gaia_median_s", "diff_s"], 1):
    cls.cell(1, c, h).font = bold
gm = {}
for line in open(inputs_path):
    pass
for i, (name, col) in enumerate([("linux", "Q"), ("windows", "R")], 2):
    cls.cell(i, 1, name).font = plain
    cls.cell(i, 3, DEFAULTS[name]).font = blue
    cls.cell(i, 2, f"=IF(COUNT(Inputs!{col}2:{col}{last})=0,C{i},MEDIAN(Inputs!{col}2:{col}{last}))").font = plain
    cls.cell(i, 4, f"=COUNT(Inputs!{col}2:{col}{last})").font = plain
    g = next((x["median_test_s"] for x in gaia["by_os_class"] if x["name"] == name), None)
cls["A5"] = "The median of the class's VMs' own medians; with no VM of the class tested, the default (internal/capacity)."
cls["A5"].font = plain

# --- Summary ------------------------------------------------------------------------------------------------------
summary["A1"] = "Proof capacity of the T24 catalog, computed from Gaia's inputs (T25 gate d)"
summary["A1"].font = title
summary["A2"] = ("Blue: inputs as Gaia reported them, and Gaia's own answer. Black: formulas. The slots (yellow) can be changed; "
                 "Gaia's column was asked with the same slots (what-if).")
summary["A2"].font = plain
lab = [("Slots", None), ("VMs (with a policy)", f"=COUNT(Inputs!A2:A{last})"), ("Demand (slot-s a day)", f"=SUM(Inputs!U2:U{last})"),
       ("Capacity (slot-s a day)", "=B4*86400"), ("Coverage", "=IF(B6=0,1,MIN(1,B7/B6))"), ("Slots needed", "=ROUNDUP(B6/86400,0)")]
gaia_vals = [gaia["slots"], gaia["vms"], gaia["demand_slot_s_per_day"], gaia["capacity_slot_s_per_day"], gaia["coverage"], gaia["slots_needed"]]
for c, h in enumerate(["", "Spreadsheet", "Gaia", "Difference"], 1):
    summary.cell(3, c, h).font = bold
for k, ((name, formula), gv) in enumerate(zip(lab, gaia_vals)):
    r = 4 + k
    summary.cell(r, 1, name).font = plain
    if formula is None:
        summary.cell(r, 2, gaia["slots"]).font = blue
        summary.cell(r, 2).fill = key
    else:
        summary.cell(r, 2, formula).font = plain
    summary.cell(r, 3, gv).font = blue
    summary.cell(r, 4, f"=B{r}-C{r}").font = plain
summary["B8"].number_format = summary["C8"].number_format = "0.0000%"
for r in (6, 7):
    for c in "BCD":
        summary[f"{c}{r}"].number_format = "#,##0.0000"

def group_table(top, label, key_col, groups):
    summary.cell(top, 1, label).font = bold
    for c, h in enumerate(["name", "VMs", "tests a day", "demand (slot-s a day)", "Gaia VMs", "Gaia tests a day", "Gaia demand", "demand difference"], 1):
        summary.cell(top + 1, c, h).font = bold
    for k, g in enumerate(groups):
        r = top + 2 + k
        summary.cell(r, 1, g["name"]).font = plain
        summary.cell(r, 2, f'=COUNTIF(Inputs!{key_col}2:{key_col}{last},A{r})').font = plain
        summary.cell(r, 3, f'=SUMIFS(Inputs!T2:T{last},Inputs!{key_col}2:{key_col}{last},A{r})').font = plain
        summary.cell(r, 4, f'=SUMIFS(Inputs!U2:U{last},Inputs!{key_col}2:{key_col}{last},A{r})').font = plain
        summary.cell(r, 5, g["vms"]).font = blue
        summary.cell(r, 6, g["tests_per_day"]).font = blue
        summary.cell(r, 7, g["demand_slot_s_per_day"]).font = blue
        summary.cell(r, 8, f"=D{r}-G{r}").font = plain
        for c in (3, 4, 6, 7, 8):
            summary.cell(r, c).number_format = "#,##0.0000"
    return top + 2 + len(groups)

nxt = group_table(12, "By OS class", "D", gaia["by_os_class"])
nxt = group_table(nxt + 1, "By policy", "C", gaia["by_policy"])
summary.cell(nxt + 1, 1, "Sum of |expected duration difference| over every VM (s)").font = bold
summary.cell(nxt + 1, 4, f"=SUMPRODUCT(ABS(Inputs!W2:W{last}))").font = plain
summary.cell(nxt + 2, 1, "Class median differences (s)").font = bold
for i, name in ((2, "linux"), (3, "windows")):
    g = next((x["median_test_s"] for x in gaia["by_os_class"] if x["name"] == name), None)
    cls.cell(i, 5, g).font = blue
    cls.cell(i, 6, f"=B{i}-E{i}").font = plain
summary.cell(nxt + 2, 4, "=ABS(Classes!F2)+ABS(Classes!F3)").font = plain
summary.cell(nxt + 3, 1, "Sources: Gaia's columns are POST /api/v1/capacity/what-if {\"slots\": %d} on the T24 catalog; the inputs are "
             "GET /api/v1/capacity/inputs, same server, same moment." % gaia["slots"]).font = plain
summary.column_dimensions["A"].width = 44
for c in "BCDEFGH":
    summary.column_dimensions[c].width = 20
wb.save(out)
print(json.dumps({"vms": n, "out": out, "group_rows_end": nxt}))
