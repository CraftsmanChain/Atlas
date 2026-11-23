#!/usr/bin/env python3
"""Build the fleet disk/memory health snapshot (node-hw-fleet-v1) from one node_hw_daily_collect day dir.

Usage: node_hw_fleet_report.py <day-dir> [<out-json>]
  Reads  <day-dir>/{nodes,status,followup,smart,storcli,sel,kern,dimm}.tsv and nodes/<ip>.txt(.gz) (##MEM section)
  Reads  the previous day dir (sibling, latest earlier date that has smart.tsv) for day-over-day deltas
  Writes <day-dir>/fleet-hw.json (or <out-json>); stdlib only, read-only toward nodes.

Rules (node-hw-rules-v1) are status/degradation rules only - no probability, no action:
  disk  critical : SMART overall failed; NVMe critical_warning>0; pending(197)>0; offline_uncorrect(198)>0;
                   reported_uncorrect(187)>0; NVMe available_spare<=10%; media_errors grew vs previous day;
                   storcli predictive failure>0
        warning  : reallocated(5) grew; wear>=80%; NVMe media_errors>0 (static); SCSI grown defects/uncorrected>0;
                   storcli media error>0
        attention: reallocated(5)>0 static; wear>=50%; CRC(199) grew; cmd_timeout(188) grew; NVMe error-log grew;
                   temperature>=60C
        (static CRC is NOT flagged - PM883/PM893 carry a model-wide historical CRC baseline)
  memory critical: SEL uncorrectable ECC in last 30d; HardwareCorrupted>0; kernel mem_offline; SEL 'Memory Device
                   Disabled' in last 30d
        warning  : SEL correctable ECC >=100 in last 7d or 'logging limit reached' in 30d; kernel EDAC/MCE lines
        attention: any SEL correctable ECC in last 30d; SEL uncorrectable ECC older than 30d
  life (SSD only): remaining_days = power_on_hours*(100-wear)/wear/24 at the lifetime-average wear rate (wear>=1).
"""
import csv, datetime as dt, glob, gzip, json, os, re, sys, urllib.parse, urllib.request
from collections import Counter, defaultdict

RULES = "node-hw-rules-v1"
csv.field_size_limit(1 << 24)
LEVEL_RANK = {"critical": 3, "warning": 2, "attention": 1, "clear": 0}


def rows(path, header=None):
    if not os.path.exists(path):
        return []
    with open(path, newline="", errors="replace") as f:
        r = csv.reader(f, delimiter="\t", quoting=csv.QUOTE_NONE)
        data = list(r)
    if header is True:
        if not data:
            return []
        h = data[0]
        return [dict(zip(h, x + [""] * (len(h) - len(x)))) for x in data[1:]]
    return data


def num(v):
    try:
        f = float(str(v).strip())
        return int(f) if f.is_integer() else f
    except (TypeError, ValueError):
        return None


def prev_day_dir(day_dir):
    root, day = os.path.dirname(os.path.abspath(day_dir)), os.path.basename(os.path.abspath(day_dir))
    cands = sorted(d for d in os.listdir(root) if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d) and d < day
                   and os.path.exists(os.path.join(root, d, "smart.tsv")))
    return os.path.join(root, cands[-1]) if cands else None


def node_text(day_dir, ip):
    for p in (os.path.join(day_dir, "nodes", ip + ".txt.gz"), os.path.join(day_dir, "nodes", ip + ".txt")):
        if os.path.exists(p):
            op = gzip.open if p.endswith(".gz") else open
            try:
                with op(p, "rt", errors="replace") as f:
                    return f.read()
            except OSError:
                return ""
    return ""


def section(text, name):
    m = re.search(r"^##%s\n(.*?)(?=^##[A-Z]|\Z)" % name, text, re.M | re.S)
    return m.group(1) if m else ""


def monitored_ips():
    """node_exporter presence in VictoriaMetrics over the last day (best effort; None if VM unreachable)."""
    url = os.environ.get("ATLAS_HW_VM_URL", "http://10.80.192.252:28481/select/0/prometheus/api/v1/query")
    out = {}
    try:
        for expr, key in (('max_over_time(up{job=~"node_exporter|node-exporter"}[1d])', "node_exporter"),
                          ('ipmi_up{collector="ipmi"}', "ipmi")):
            q = url + "?" + urllib.parse.urlencode({"query": expr})
            res = json.load(urllib.request.urlopen(q, timeout=30))["data"]["result"]
            for r in res:
                m = r["metric"]
                ip = m.get("host_ip") or re.sub(r":\d+$", "", m.get("instance", ""))
                out.setdefault(ip, {})[key] = r["value"][1] == "1"
        return out
    except Exception as e:  # noqa: BLE001 - optional enrichment
        print("vm enrichment skipped: %s" % e, file=sys.stderr)
        return None


def worst(levels):
    return max(levels, key=lambda x: LEVEL_RANK[x]) if levels else "clear"


def main():
    day_dir = sys.argv[1].rstrip("/")
    out_path = sys.argv[2] if len(sys.argv) > 2 else os.path.join(day_dir, "fleet-hw.json")
    day = os.path.basename(day_dir)
    day_date = dt.date.fromisoformat(day)
    prev = prev_day_dir(day_dir)

    nodes = {r[0]: {"kind": r[1], "host": r[2] if len(r) > 2 else ""} for r in rows(os.path.join(day_dir, "nodes.tsv")) if r}
    status = {r["ip"]: r for r in rows(os.path.join(day_dir, "status.tsv"), True)}
    follow = defaultdict(list)
    for r in rows(os.path.join(day_dir, "followup.tsv"), True):
        follow[r["ip"]].append(r["category"])
    mon = monitored_ips()

    # ---------------- disks ----------------
    smart = rows(os.path.join(day_dir, "smart.tsv"), True)
    prev_smart = {r["serial"]: r for r in rows(os.path.join(prev, "smart.tsv"), True) if r.get("serial")} if prev else {}
    storcli = {}
    for r in rows(os.path.join(day_dir, "storcli.tsv")):
        if len(r) >= 10 and r[7]:
            storcli[r[7].strip()] = {"slot": r[1], "state": r[2], "media_err": num(r[3]), "other_err": num(r[4]), "pred_fail": num(r[5])}
    disks = []
    for r in smart:
        ip = r["ip"]
        d = {"ip": ip, "host": nodes.get(ip, {}).get("host", ""), "kind": nodes.get(ip, {}).get("kind", ""),
             "dev": r["dev"] + ("" if r["dtype"] in ("", "sat", "nvme") else " " + r["dtype"]),
             "proto": r["protocol"], "model": r["model"], "serial": r["serial"], "fw": r["firmware"],
             "cap_gb": num(r["capacity_gb"]), "poh": num(r["power_on_hours"]), "temp": num(r["temp_c"]),
             "passed": r["smart_passed"], "wear": num(r["wear_pct"]), "spare": num(r["spare_pct"]),
             "crit": num(r["crit_warning"]), "media": num(r["media_errors"]), "errlog": num(r["err_log_entries"]),
             "realloc": num(r["realloc"]), "unc": num(r["reported_uncorrect"]), "timeout": num(r["cmd_timeout"]),
             "pending": num(r["pending"]), "offunc": num(r["offline_uncorrect"]), "crc": num(r["crc"]),
             "grown": num(r["scsi_grown_defects"]), "scsi_unc": num(r["scsi_uncorrected_rw"]),
             "written_tb": num(r["written_tb"]), "delta": {}}
        p = prev_smart.get(r["serial"]) if r["serial"] else None
        if p:
            for k, col in (("realloc", "realloc"), ("media", "media_errors"), ("crc", "crc"), ("pending", "pending"),
                           ("timeout", "cmd_timeout"), ("errlog", "err_log_entries"), ("wear", "wear_pct")):
                a, b = num(r[col]), num(p[col])
                if a is not None and b is not None and a != b:
                    d["delta"][k] = a - b
        sc = storcli.get(r["serial"])
        if sc:
            d["raid"] = sc
        hits = []
        def hit(level, code):
            hits.append([level, code])
        if d["passed"] == "false": hit("critical", "smart_overall_failed")
        if d["passed"] == "read_error": hit("attention", "smart_read_error")
        if (d["crit"] or 0) > 0: hit("critical", "nvme_critical_warning")
        if (d["pending"] or 0) > 0: hit("critical", "pending_sectors")
        if (d["offunc"] or 0) > 0: hit("critical", "offline_uncorrectable")
        if (d["unc"] or 0) > 0: hit("critical", "reported_uncorrectable")
        if d["proto"] == "NVMe" and d["spare"] is not None and d["spare"] <= 10: hit("critical", "nvme_spare_low")
        if d["delta"].get("media", 0) > 0: hit("critical", "media_errors_new")
        if sc and (sc["pred_fail"] or 0) > 0: hit("critical", "raid_predictive_failure")
        if d["delta"].get("realloc", 0) > 0: hit("warning", "reallocated_new")
        if (d["wear"] or 0) >= 80: hit("warning", "wear_ge_80")
        if (d["media"] or 0) > 0 and not d["delta"].get("media"): hit("warning", "media_errors_present")
        if (d["grown"] or 0) > 0 or (d["scsi_unc"] or 0) > 0: hit("warning", "scsi_defects")
        if sc and (sc["media_err"] or 0) > 0: hit("warning", "raid_media_error")
        if (d["realloc"] or 0) > 0 and not d["delta"].get("realloc"): hit("attention", "reallocated_present")
        if 50 <= (d["wear"] or 0) < 80: hit("attention", "wear_ge_50")
        if d["delta"].get("crc", 0) > 0: hit("attention", "crc_new")
        if d["delta"].get("timeout", 0) > 0: hit("attention", "cmd_timeout_new")
        if d["delta"].get("errlog", 0) > 0: hit("attention", "nvme_errlog_new")
        if (d["temp"] or 0) >= 60: hit("attention", "temperature_ge_60")
        d["hits"] = [h[1] for h in sorted(hits, key=lambda h: -LEVEL_RANK[h[0]])]
        d["level"] = worst([h[0] for h in hits])
        if d["wear"] and d["wear"] >= 1 and d["poh"]:
            d["life_days"] = int(d["poh"] * (100 - d["wear"]) / d["wear"] / 24)
        disks.append(d)

    # ---------------- memory ----------------
    sel = defaultdict(lambda: {"ce_7d": 0, "ce_30d": 0, "ue_30d": 0, "ue_total": 0, "ue_last": "", "ce_last": "",
                               "disabled_30d": 0, "limit_30d": 0, "locs": Counter()})
    for r in rows(os.path.join(day_dir, "sel.tsv")):
        if len(r) < 6 or not r[4].startswith("Memory") or (len(r) > 6 and r[6].strip() == "Deasserted"):
            continue
        m = re.fullmatch(r"(\d\d)/(\d\d)/(\d{4})", r[2])
        if not m:
            continue
        try:
            date = dt.date(int(m.group(3)), int(m.group(1)), int(m.group(2)))
        except ValueError:
            continue
        age = (day_date - date).days
        ev, s = r[5], sel[r[0]]
        loc = re.search(r"@([^)]*\))", ev) or re.search(r"Memory (\S+)$", r[4])
        iso = date.isoformat()
        if "Uncorrectable ECC" in ev:
            s["ue_total"] += 1
            s["ue_last"] = max(s["ue_last"], iso)
            if 0 <= age <= 30: s["ue_30d"] += 1
        elif "logging limit reached" in ev:
            if 0 <= age <= 30: s["limit_30d"] += 1
        elif "Correctable ECC" in ev:
            s["ce_last"] = max(s["ce_last"], iso)
            if 0 <= age <= 30: s["ce_30d"] += 1
            if 0 <= age <= 7: s["ce_7d"] += 1
        elif "Memory Device Disabled" in ev:
            if 0 <= age <= 30: s["disabled_30d"] += 1
        else:
            continue
        if loc and not loc.group(1).startswith("#"):
            s["locs"][loc.group(1)] += 1
    kern = defaultdict(Counter)
    for r in rows(os.path.join(day_dir, "kern.tsv")):
        if len(r) >= 4:
            kern[r[0]][r[2]] += num(r[3]) or 0
    dimms = defaultdict(lambda: [0, 0])
    for r in rows(os.path.join(day_dir, "dimm.tsv")):
        if len(r) >= 3:
            g = num(r[2].split()[0]) if r[2].split() else None
            dimms[r[0]][0] += 1
            dimms[r[0]][1] += g or 0
    memory = []
    for ip, st in sorted(status.items()):
        if st["status"] not in ("ok",) and not st["status"].startswith("partial"):
            continue
        txt = section(node_text(day_dir, ip), "MEM")
        hc = re.search(r"HardwareCorrupted:\s+(\d+)", txt)
        s, k = sel.get(ip), kern.get(ip, Counter())
        m = {"ip": ip, "host": nodes.get(ip, {}).get("host", ""), "kind": nodes.get(ip, {}).get("kind", ""),
             "dimms": dimms[ip][0], "mem_gb": dimms[ip][1], "hw_corrupted_kb": int(hc.group(1)) if hc else None,
             "edac": k.get("edac", 0), "mce": k.get("mce", 0), "mem_offline": k.get("mem_offline", 0)}
        if s:
            m.update({x: s[x] for x in ("ce_7d", "ce_30d", "ue_30d", "ue_total", "ue_last", "ce_last", "disabled_30d", "limit_30d")})
            m["locs"] = [l for l, _ in s["locs"].most_common(6)]
        hits = []
        def hit(level, code):
            hits.append([level, code])
        if m.get("ue_30d"): hit("critical", "sel_ue_30d")
        if (m["hw_corrupted_kb"] or 0) > 0: hit("critical", "hardware_corrupted")
        if m["mem_offline"]: hit("critical", "kernel_page_offline")
        if m.get("disabled_30d"): hit("critical", "dimm_disabled_30d")
        if m.get("ce_7d", 0) >= 100: hit("warning", "sel_ce_storm_7d")
        if m.get("limit_30d"): hit("warning", "sel_ce_limit_reached")
        if m["edac"] or m["mce"]: hit("warning", "kernel_edac_mce")
        if m.get("ce_30d") and m.get("ce_7d", 0) < 100: hit("attention", "sel_ce_30d")
        if m.get("ue_total") and not m.get("ue_30d"): hit("attention", "sel_ue_history")
        m["hits"] = [h[1] for h in sorted(hits, key=lambda h: -LEVEL_RANK[h[0]])]
        m["level"] = worst([h[0] for h in hits])
        memory.append(m)

    # ---------------- coverage ----------------
    node_rows = []
    for ip, n in sorted(nodes.items(), key=lambda x: [int(p) for p in x[0].split(".")]):
        st = status.get(ip, {})
        row = {"ip": ip, "host": n["host"], "kind": n["kind"], "status": st.get("status", "missing"),
               "flags": [f for f in st.get("flags", "").split(",") if f], "followup": sorted(set(follow.get(ip, [])))}
        if mon is not None:
            mm = mon.get(ip, {})
            row["node_exporter"] = mm.get("node_exporter")
            row["ipmi"] = mm.get("ipmi")
        node_rows.append(row)
    by_kind = defaultdict(Counter)
    for r in node_rows:
        by_kind[r["kind"]][r["status"]] += 1
    flags = Counter(f.split(":")[0] if f.startswith("stopped_in") else f for r in node_rows for f in r["flags"])
    smart_nodes = {d["ip"] for d in disks}
    collected = [r for r in node_rows if r["status"] == "ok" or r["status"].startswith("partial")]
    excl = Counter(r[3] if len(r) > 3 else "" for r in rows(os.path.join(day_dir, "excluded.tsv")) if r) \
        if os.path.exists(os.path.join(day_dir, "excluded.tsv")) else Counter()
    cov = {"nodes_total": len(node_rows), "collected": len(collected),
           "excluded": sum(excl.values()), "excluded_by_reason": dict(excl),
           "by_status": dict(Counter(r["status"] for r in node_rows)),
           "by_kind": {k: dict(v) for k, v in by_kind.items()},
           "flags": dict(flags.most_common()),
           "followup": dict(Counter(c for r in node_rows for c in r["followup"])),
           "smart_nodes": len(smart_nodes), "smart_disks": len(disks),
           "collected_without_smart": len([r for r in collected if r["ip"] not in smart_nodes]),
           "failed_not_in_vm": None if mon is None else len([r for r in node_rows if r not in collected and r.get("node_exporter") is None and r.get("ipmi") is None])}

    def lv(items):
        c = Counter(i["level"] for i in items)
        return {k: c.get(k, 0) for k in ("critical", "warning", "attention", "clear")}
    rulec = Counter(h for d in disks for h in d["hits"])
    rulem = Counter(h for m in memory for h in m["hits"])
    models = Counter(d["model"] for d in disks)
    started = None
    try:
        started = dt.datetime.fromtimestamp(os.path.getmtime(os.path.join(day_dir, "nodes.tsv")), dt.timezone.utc).isoformat()
    except OSError:
        pass
    report = {"schema": "node-hw-fleet-v1", "rule_version": RULES, "day": day,
              "previous_day": os.path.basename(prev) if prev else None,
              "collected_at": started, "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "read_only": True, "actions_executed": False,
              "coverage": cov,
              "summary": {"disk": {"total": len(disks), "by_level": lv(disks), "by_rule": dict(rulec.most_common()),
                                   "by_protocol": dict(Counter(d["proto"] or "unknown" for d in disks)),
                                   "with_delta_baseline": len([d for d in disks if d["serial"] in prev_smart]),
                                   "models": [[m, c] for m, c in models.most_common(15)]},
                          "memory": {"nodes": len(memory), "by_level": lv(memory), "by_rule": dict(rulem.most_common()),
                                     "dimms": sum(m["dimms"] for m in memory)}},
              "disks": disks, "memory": memory, "nodes": node_rows}
    tmp = out_path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(report, f, ensure_ascii=False, separators=(",", ":"))
    os.replace(tmp, out_path)
    print("fleet-hw %s: disks %d %s; memory nodes %d %s; prev=%s" % (day, len(disks), lv(disks), len(memory), lv(memory), report["previous_day"]))


if __name__ == "__main__":
    main()
