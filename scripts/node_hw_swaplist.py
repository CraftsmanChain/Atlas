#!/usr/bin/env python3
"""Print the pre-replacement DIMM list for one node (memory plan v1.2 section 2.1). Read-only, stdlib only.

Usage: node_hw_swaplist.py <ip> [--root /data/atlas-research/node-hw-daily] [--day YYYY-MM-DD]
Columns: slot, SN, part, size, CE 30d, first CE, UE 30d / total, disabled 30d, DIMM rules, node level, mapped.
Also served by the Atlas API: GET /api/v1/hardware/memory/swaplist?ip=<ip>[&day=YYYY-MM-DD].
Ops compare the SN on the pulled DIMM's label with this list and register the swap in
node-hw-ledger/dimm_replacements.ops.tsv (template deploy/node-hw/dimm-replacement-template.tsv).
"""
import argparse, json, os, re, sys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("ip")
    ap.add_argument("--root", default=os.environ.get("ATLAS_HW_COLLECT_ROOT", "/data/atlas-research/node-hw-daily"))
    ap.add_argument("--day")
    a = ap.parse_args()
    days = sorted(d for d in os.listdir(a.root) if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d)
                  and os.path.exists(os.path.join(a.root, d, "fleet-hw.json")))
    day = a.day or (days[-1] if days else None)
    if not day:
        sys.exit("no fleet-hw.json under %s" % a.root)
    rep = json.load(open(os.path.join(a.root, day, "fleet-hw.json")))
    m = next((x for x in rep.get("memory", []) if x["ip"] == a.ip), None)
    if not m:
        sys.exit("%s not in the %s snapshot (not collected or out of scope)" % (a.ip, day))
    print("# %s %s  board=%s  level=%s  hits=%s  snapshot=%s rules=%s" % (m["ip"], m.get("host", ""), m.get("board", ""), m["level"],
          ",".join(m.get("hits", [])), day, rep.get("rule_version")))
    print("\t".join(["slot", "sn", "part", "size", "ce_30d", "first_ce", "ue_30d/total", "disabled_30d", "dimm_rules", "mapped"]))
    for e in m.get("dimm_issues", []):
        print("\t".join(str(x) for x in (e.get("dmi_slot") or e.get("slot") or e.get("sensor", ""), e.get("sn", ""), e.get("part", ""),
                                         e.get("size", ""), e.get("ce_30d", 0), e.get("first_ce", ""),
                                         "%s/%s" % (e.get("ue_30d", 0), e.get("ue_total", 0)), e.get("disabled_30d", 0),
                                         ",".join(e.get("hits", [])), "yes" if e.get("mapped") else "no (SN unknown)")))


if __name__ == "__main__":
    main()
