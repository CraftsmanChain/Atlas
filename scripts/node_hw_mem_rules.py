#!/usr/bin/env python3
"""Memory rules v2 (node-hw-rules-v2.0, memory plan v1.1 section 4 as amended by v1.2). Stdlib only, read-only.

Used by node_hw_fleet_report.py. Three independent outputs per collected node (plan v1.1 section 4):
  presence      MEM-S1..S4   (DIMM missing / replaced / capacity drop / configuration)
  observability MEM-O1..O7   full | limited | blind  ("不可见 != 正常": a clear node on a CE-blind platform is limited)
  health        MEM-H1..H15  critical / warning / attention, per node and per DIMM
Levels are status / degradation rules only - no probability, no action (U4).

Inputs other than the day dir: the cross-day ledger (node_hw_ledger.py), the visibility matrix
configs/node-hw-mem-platforms.tsv, VictoriaMetrics (BMC SEL counters, DIMM sensors, SEL size history) and the
optional Redfish poll result <day>/redfish-mem.json (scripts/node_hw_redfish_probe.py --daily on 10.80.32.234).
"""
import csv, datetime as dt, json, os, re, sys, urllib.parse, urllib.request
from collections import Counter, defaultdict

RULES = "node-hw-rules-v2.0"
LEVEL_RANK = {"critical": 3, "warning": 2, "attention": 1, "clear": 0}
TZ = dt.timezone(dt.timedelta(hours=8))
RULE_LEVEL = {
    "mem_h1_ue": "critical", "mem_h2_disabled": "critical", "mem_h3_bad_page": "critical",
    "mem_h4_ce_storm": "warning", "mem_h5_ce_limit": "warning", "mem_h6_ce_persistent": "warning",
    "mem_h7_channel_multi": "warning", "mem_h8_ce_after_swap": "warning", "mem_h9_edac": "warning",
    "mem_h14_platform_ce": "warning", "mem_h15_bmc_health": "warning", "mem_s1_dimm_missing": "warning",
    "mem_s3_capacity_drop": "warning",
    "mem_h10_ce_rising": "attention", "mem_h11_ce": "attention", "mem_h12_ue_history": "attention",
    "mem_h13_mce_undecoded": "attention", "mem_s4_config": "attention",
}
DIMM_RULES = ("mem_h4_ce_storm", "mem_h6_ce_persistent", "mem_h7_channel_multi", "mem_h8_ce_after_swap",
              "mem_h10_ce_rising", "mem_h15_bmc_health")
# thresholds (frozen with the rule version; plan: tune by "weekly new DIMM warnings <= 5", not by backtest)
H4_24H, H4_7D, H6_DAYS, H10_MIN, H14_DIMMS = 50, 100, 3, 10, 8
REDFISH_PLATFORMS = ("ue_replaced", "dimm_health")  # MEM-O7 applies only where Redfish is a needed source


def vm_base():
    u = os.environ.get("ATLAS_HW_VM_URL", "http://10.80.192.252:28481/select/0/prometheus/api/v1/query")
    return re.sub(r"/query(_range)?$", "", u)


def vm_query(expr, at=None, timeout=60):
    q = {"query": expr}
    if at is not None:
        q["time"] = "%d" % at.timestamp()
    url = vm_base() + "/query?" + urllib.parse.urlencode(q)
    return json.load(urllib.request.urlopen(url, timeout=timeout))["data"]["result"]


def vm_range(expr, start, end, step=300, timeout=120):
    q = {"query": expr, "start": "%d" % start.timestamp(), "end": "%d" % end.timestamp(), "step": str(step)}
    url = vm_base() + "/query_range?" + urllib.parse.urlencode(q)
    return json.load(urllib.request.urlopen(url, timeout=timeout))["data"]["result"]


# ---------------------------------------------------------------- visibility matrix (R-6)
def platforms_path():
    p = os.environ.get("ATLAS_HW_MEM_PLATFORMS")
    if p:
        return p
    here = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "configs", "node-hw-mem-platforms.tsv")
    return here if os.path.exists(here) else "/ops/atlas/configs/node-hw-mem-platforms.tsv"


def load_platforms(path=None):
    path = path or platforms_path()
    out, version = [], ""
    if not os.path.exists(path):
        return out, "missing"
    with open(path, errors="replace") as f:
        for line in f:
            line = line.rstrip("\n")
            m = re.search(r"Matrix version: ([\w.-]+)", line)
            if m:
                version = m.group(1)
            if not line or line.startswith("#") or line.startswith("board\t"):
                continue
            c = line.split("\t") + [""] * 8
            out.append({"board_re": re.compile(c[0], re.I), "product_re": None if c[1] in ("", "*") else re.compile(c[1], re.I),
                        "platform": c[2], "ce_source": c[3], "ceiling": c[4], "redfish": c[5], "verified_at": c[6], "evidence": c[7]})
    return out, version or os.path.basename(path)


def platform_of(matrix, board, product):
    for r in matrix:
        if r["board_re"].fullmatch(board.strip()) and (r["product_re"] is None or r["product_re"].fullmatch(product.strip())):
            return r
    return None


# ---------------------------------------------------------------- DIMM helpers
def channel_of(d):
    """(cpu, channel) for MEM-H7 from a dmidecode record, or None."""
    if not d:
        return None
    slot, bank = d.get("slot", "").upper(), d.get("bank", "").upper()
    m = re.match(r"P(\d+) CHANNEL ([A-Z])$", bank)                     # GOOXI: bank 'P0 CHANNEL A', slot 'DIMM 0'
    if m:
        return (m.group(1), m.group(2))
    m = re.match(r"^P(\d+)[-_]DIMM([A-Z])\d+$", slot) or re.match(r"^CPU(\d+)_DIMM_?([A-Z])\d+$", slot) \
        or re.match(r"^DIMM_P(\d+)_([A-Z])\d+$", slot)
    if m:
        return (m.group(1), m.group(2))
    m = re.match(r"^DIMM([A-Z])\d+\(CPU(\d+)\)$", slot)
    if m:
        return (m.group(2), m.group(1))
    m = re.match(r"^DIMM_?([A-P])\d+$", slot)                          # R8428 / KMPG: A..H = CPU0, I..P = CPU1
    if m:
        return ("0" if m.group(1) <= "H" else "1", m.group(1))
    return None


def cpu_of(d, text_loc=""):
    ch = channel_of(d)
    if ch:
        return ch[0]
    m = re.search(r"CPU ?(\d+)|P(\d+)", text_loc or "", re.I)
    return (m.group(1) or m.group(2)) if m else None


def size_gb(s):
    m = re.match(r"(\d+)\s*(GB|MB|TB)", s or "", re.I)
    if not m:
        return None
    v = int(m.group(1))
    return v / 1024 if m.group(2).upper() == "MB" else v * 1024 if m.group(2).upper() == "TB" else v


def mts(s):
    m = re.match(r"(\d+)\s*(MT/s|MHz)", s or "")
    return int(m.group(1)) if m else None


# ---------------------------------------------------------------- Redfish poll result
def load_redfish(day_dir):
    p = os.path.join(day_dir, "redfish-mem.json")
    if not os.path.exists(p):
        return None
    try:
        j = json.load(open(p))
    except (OSError, ValueError):
        return None
    return {t.get("host_ip"): t for t in j.get("targets", []) if t.get("host_ip")}


def redfish_ledger_records(rf):
    """Vendor-log UE / disabled events -> ledger records (source=redfish). sel_id = 'rf:' + compact timestamp + slot,
    so a re-poll of the same log entry maps to the same ledger key."""
    out = []
    for ip, t in (rf or {}).items():
        for e in t.get("events", []):
            if e.get("kind") not in ("ue", "disabled", "ce"):
                continue
            created = e.get("created", "")
            m = re.match(r"(\d{4})[-/](\d\d)[-/](\d\d)[T ](\d\d:\d\d:\d\d)", created)
            if not m:
                continue
            date, time = "%s-%s-%s" % m.group(1, 2, 3), m.group(4)
            sid = "rf:%s%s%s%s:%s" % (m.group(1), m.group(2), m.group(3), time.replace(":", ""), e.get("slot", ""))
            out.append({"ip": ip, "sel_id": sid, "date": date, "time": time, "kind": e["kind"], "text_loc": e.get("slot", ""),
                        "sensor": "", "raw": (e.get("message") or "")[:200], "slot": e.get("slot", ""), "bank": "",
                        "sn": e.get("sn", ""), "part": "", "size": "", "vendor": "", "via": "redfish", "bmc_date": "",
                        "time_valid": "1", "source": "redfish"})
    return out


# ---------------------------------------------------------------- SEL clears (R-5)
def vm_clears(start, end):
    """ipmi_sel_logs_count drops: v[i] <= 0.7 * v[i-1], drop >= 20, and the next 6 samples (30 min) stay below
    0.7 * v[i-1] (one-off partial BMC reads are not clears). Returns [{ip, time, pre, post}] or None if VM failed."""
    try:
        res = vm_range("max by (host_ip) (ipmi_sel_logs_count)", start, end, 300)
    except Exception as e:  # noqa: BLE001
        print("vm sel clears skipped: %s" % e, file=sys.stderr)
        return None
    out = []
    for s in res:
        ip = s["metric"].get("host_ip")
        v = [(int(float(t)), float(x)) for t, x in s["values"]]
        for i in range(1, len(v)):
            pre, cur = v[i - 1][1], v[i][1]
            if pre - cur >= 20 and cur <= 0.7 * pre:
                nxt = [x for _, x in v[i + 1:i + 7]]
                if all(x < 0.7 * pre for x in nxt):
                    out.append({"ip": ip, "time": dt.datetime.fromtimestamp(v[i][0], TZ).strftime("%Y-%m-%d %H:%M:%S"),
                                "pre": int(pre), "post": int(cur)})
    return out


def selinfo_rows(day_dir):
    p = os.path.join(day_dir, "selinfo.tsv")
    out = {}
    if not os.path.exists(p):
        return out
    with open(p, errors="replace") as f:
        head = f.readline().rstrip("\n").split("\t")
        for line in f:
            c = line.rstrip("\n").split("\t")
            out[c[0]] = dict(zip(head, c + [""] * (len(head) - len(c))))
    return out


def atlas_resets(ledger_root):
    p = os.path.join(ledger_root, "sel_reset_events.tsv")
    out = defaultdict(list)
    if os.path.exists(p):
        with open(p, errors="replace") as f:
            next(f, None)
            for line in f:
                c = line.rstrip("\n").split("\t")
                if len(c) > 1:
                    try:
                        out[c[0]].append(dt.datetime.strptime(c[1], "%Y-%m-%d %H:%M:%S").replace(tzinfo=TZ))
                    except ValueError:
                        pass
    return out


def _bmc_dt(s):
    for f in ("%m/%d/%Y %H:%M:%S",):
        try:
            return dt.datetime.strptime(s.strip(), f).replace(tzinfo=TZ)
        except ValueError:
            pass
    return None


def detect_clears(day, today_si, prev_si, vm_events, resets, ref):
    """MEM-O6 candidates for today: sel info entries drop / Last Del Time change vs the previous collected day, plus
    VM drops. by=atlas when an Atlas rotation (sel_reset_events.tsv) is within 2 h before .. 1 h after."""
    evs = []
    for ip, s in today_si.items():
        p = prev_si.get(ip)
        if not p:
            continue
        try:
            e0, e1 = int(p.get("entries") or -1), int(s.get("entries") or -1)
        except ValueError:
            e0 = e1 = -1
        ld0, ld1 = p.get("last_del", "").strip(), s.get("last_del", "").strip()
        dropped = e0 > 0 and 0 <= e1 < e0
        del_changed = ld1 and ld1 != ld0 and _bmc_dt(ld1) is not None
        if dropped or del_changed:
            t = _bmc_dt(ld1) if del_changed else None
            evs.append({"ip": ip, "time": (t or ref).strftime("%Y-%m-%d %H:%M:%S"), "source": "selinfo",
                        "pre": str(e0) if e0 >= 0 else "", "post": str(e1) if e1 >= 0 else ""})
    for v in vm_events or []:
        evs.append({"ip": v["ip"], "time": v["time"], "source": "vm", "pre": str(v["pre"]), "post": str(v["post"])})
    for e in evs:
        t = dt.datetime.strptime(e["time"], "%Y-%m-%d %H:%M:%S").replace(tzinfo=TZ)
        e["by"] = "atlas" if any(-3600 <= (r - t).total_seconds() <= 7200 or -3600 <= (t - r).total_seconds() <= 3600
                                 for r in resets.get(e["ip"], [])) else "external"
    return evs


# ---------------------------------------------------------------- BMC counter reconciliation (R-4)
def bmc_counts(ref):
    """Per host: BMC-side SEL counters from ipmi_exporter at the collection time (last sample within the 3 h before
    ref + 20 min; the exporter's sel-events collector reports sparsely) and the max over the day (for reference only:
    a SEL cleared during the day makes the 1-day max larger than what the collection saw - 11-27 finding).
    ipmi_exporter's correctable_ecc also matches 'Uncorrectable ECC' -> CE = ce - ue."""
    out = defaultdict(dict)
    sel = 'ipmi_sel_events_count_by_name{name=~"correctable_ecc|uncorrectable_ecc"}'
    for key, expr, at in (("near", "max by (host_ip, name) (last_over_time(%s[3h]))" % sel, ref + dt.timedelta(minutes=20)),
                          ("day", "max by (host_ip, name) (max_over_time(%s[1d]))" % sel, ref)):
        for r in vm_query(expr, at):
            out[r["metric"].get("host_ip")]["%s_%s" % (key, r["metric"].get("name"))] = int(float(r["value"][1]))
    return out


def reconcile(counts, atlas_sel, unparsed, flags_by_ip, clears_today, collected):
    rows = []
    for ip in sorted(set(counts) | set(atlas_sel), key=lambda x: [int(p) if p.isdigit() else 0 for p in x.split(".")]):
        if ip not in collected:
            continue
        c = counts.get(ip, {})
        a = atlas_sel.get(ip, Counter())
        if "near_correctable_ecc" not in c and not a:
            continue
        b_ue = c.get("near_uncorrectable_ecc", 0)
        b_ce = c.get("near_correctable_ecc", 0) - b_ue
        a_ce, a_ue = a.get("ce", 0) + a.get("limit", 0), a.get("ue", 0)
        tol = lambda x: max(2, round(0.05 * x))  # noqa: E731
        reasons = []
        if "near_correctable_ecc" not in c:
            reasons.append("no_bmc_series")
        ok = "near_correctable_ecc" in c and abs(b_ce - a_ce) <= tol(max(b_ce, a_ce)) and abs(b_ue - a_ue) <= tol(max(b_ue, a_ue))
        if not ok:
            if unparsed.get(ip):
                reasons.append("unparsed_%d" % sum(unparsed[ip].values()))
            fl = flags_by_ip.get(ip, "")
            for f in ("sel_full", "sel_incomplete", "sel_list_error", "sel_time_invalid"):
                if f in fl:
                    reasons.append(re.search(r"%s[^,]*" % f, fl).group(0))
            if ip in clears_today:
                reasons.append("sel_cleared_today")
            if c.get("day_correctable_ecc", 0) > c.get("near_correctable_ecc", 0):
                reasons.append("bmc_count_changed_during_day")
            if a.get("deasserted"):
                reasons.append("deasserted_%d" % a["deasserted"])
        rows.append({"ip": ip, "bmc_ce": b_ce, "bmc_ue": b_ue, "atlas_ce": a_ce, "atlas_ue": a_ue,
                     "atlas_limit": a.get("limit", 0), "bmc_ce_1d": c.get("day_correctable_ecc", 0) - c.get("day_uncorrectable_ecc", 0),
                     "verdict": "ok" if ok else ("no_bmc_series" if "near_correctable_ecc" not in c else "mismatch"),
                     "reasons": ",".join(reasons)})
    return rows


# ---------------------------------------------------------------- DIMM sensors from ipmi_exporter (MEM-H15)
def bmc_dimm_sensors(ref):
    """Memory-type ipmi sensors with state > 0 over the day, keeping only series whose (host, sensor id) -> name
    mapping is stable (11-27 A6: XFUSION alternates two SDR mappings -> state=2 is an artefact). Returns
    (alerts {ip: [names]}, unstable {ip: n}, sticky {ip: [names]})."""
    res = vm_query('max by (host_ip, id, name) (max_over_time(ipmi_sensor_state{type="Memory"}[1d]))', ref)
    names = defaultdict(set)
    ids_of = defaultdict(set)
    for r in res:
        m = r["metric"]
        names[(m.get("host_ip"), m.get("id"))].add(m.get("name"))
        ids_of[(m.get("host_ip"), m.get("name"))].add(m.get("id"))
    alerts, unstable, sticky = defaultdict(list), Counter(), defaultdict(list)
    for r in res:
        m = r["metric"]
        if float(r["value"][1]) <= 0:
            continue
        ip, sid, nm = m.get("host_ip"), m.get("id"), m.get("name", "")
        if len(names[(ip, sid)]) > 1 or len(ids_of[(ip, nm)]) > 1:
            unstable[ip] += 1
            continue
        if re.fullmatch(r"CPU\d_ECC\d", nm):
            sticky[ip].append(nm)       # 4090 CPUn_ECCn: summary sensor, suspected sticky; not a DIMM finding
            continue
        if re.search(r"DIMM", nm, re.I):
            alerts[ip].append(nm)
    return alerts, unstable, sticky


# ---------------------------------------------------------------- history (mem_state.tsv per day)
STATE_COLS = ["ip", "dimms", "memtotal_kb", "hw_corrupted_kb", "min_dimm_gb"]


def write_state(day_dir, rows):
    with open(os.path.join(day_dir, "mem_state.tsv"), "w") as f:
        f.write("\t".join(STATE_COLS) + "\n")
        for r in rows:
            f.write("\t".join(str(r.get(c, "")) for c in STATE_COLS) + "\n")


def read_history(day_dir, days=14):
    root, day = os.path.dirname(os.path.abspath(day_dir)), os.path.basename(os.path.abspath(day_dir))
    lo = (dt.date.fromisoformat(day) - dt.timedelta(days=days)).isoformat()
    hist = defaultdict(list)
    for d in sorted(x for x in os.listdir(root) if re.fullmatch(r"\d{4}-\d{2}-\d{2}", x) and lo <= x < day):
        p = os.path.join(root, d, "mem_state.tsv")
        if os.path.exists(p):
            with open(p) as f:
                next(f, None)
                for line in f:
                    c = line.rstrip("\n").split("\t")
                    r = dict(zip(STATE_COLS, c))
                    hist[r["ip"]].append(dict(r, day=d))
        else:  # days before rules v2: DIMM count only, from dimm.tsv
            p = os.path.join(root, d, "dimm.tsv")
            if os.path.exists(p):
                n = Counter(line.split("\t", 1)[0] for line in open(p, errors="replace") if line.strip())
                for ip, k in n.items():
                    hist[ip].append({"ip": ip, "dimms": str(k), "memtotal_kb": "", "hw_corrupted_kb": "", "day": d})
    return hist


def _int(v):
    try:
        return int(float(v))
    except (TypeError, ValueError):
        return None


# ---------------------------------------------------------------- replacements (R-8)
def dmidecode_swaps(ip, today, prev, day, valid_sn):
    """Same node, same slot (locator + bank), SN changed between the previous collected day and today (P-2)."""
    out = []
    pmap = {(d["slot"], d.get("bank", "")): d for d in prev}
    for d in today:
        p = pmap.get((d["slot"], d.get("bank", "")))
        if p and valid_sn(p["sn"]) and valid_sn(d["sn"]) and p["sn"] != d["sn"]:
            out.append({"ip": ip, "slot": d["slot"] + (" / " + d["bank"] if d.get("bank") else ""), "old_sn": p["sn"],
                        "new_sn": d["sn"], "date": day, "reason": "", "basis": "", "sn_match": "unknown",
                        "source": "dmidecode", "note": "detected between previous collection and %s" % day})
    return out


def ibmc_swaps(rf):
    out = []
    for ip, t in (rf or {}).items():
        for e in t.get("events", []):
            if e.get("kind") == "replaced" and e.get("old_sn"):
                out.append({"ip": ip, "slot": e.get("slot", ""), "old_sn": e["old_sn"], "new_sn": e.get("new_sn", ""),
                            "date": (e.get("created") or "")[:10], "reason": "", "basis": "", "sn_match": "unknown",
                            "source": "ibmc", "note": e.get("message_id", "")})
    return out


# ---------------------------------------------------------------- per-node evaluation
def worst(levels):
    return max(levels, key=lambda x: LEVEL_RANK[x]) if levels else "clear"


def dimm_key(e):
    return ("sn:" + e["sn"]) if e.get("sn") else ("loc:" + (e.get("text_loc") or ("#" + e["sensor"] if e.get("sensor") else "#unknown")))


def evaluate_node(ip, ctx, base):
    """base: dict with ip/host/kind/board/product/dimm_rec/hw_corrupted_kb/memtotal_kb/edac/mce/mem_offline/flags.
    Returns the memory row (node-hw-fleet-v1 memory[] entry, rules v2)."""
    day, ref = ctx["day"], ctx["ref"]
    d0 = dt.date.fromisoformat(day)
    rows = ctx["ledger_by_ip"].get(ip, [])
    dimm_rec = base["dimm_rec"]
    by_sn = {d["sn"]: d for d in dimm_rec if d.get("sn")}
    repl = ctx["repl_by_ip"].get(ip, [])
    replaced_old = {r["old_sn"]: r for r in repl}
    replaced_new = {r["new_sn"]: r for r in repl if r.get("new_sn")}
    clears = [c for c in ctx["clears_by_ip"].get(ip, []) if (d0 - dt.date.fromisoformat(c["time"][:10])).days <= 7]

    def age(iso):
        return (d0 - dt.date.fromisoformat(iso)).days

    m = {k: base[k] for k in ("ip", "host", "kind", "board", "hw_corrupted_kb", "edac", "mce", "mem_offline")}
    m.update({"product": base.get("product", ""), "dimms": len(dimm_rec), "mem_gb": round(sum(size_gb(d["size"]) or 0 for d in dimm_rec)),
              "memtotal_kb": base.get("memtotal_kb")})
    node = {"ce_7d": 0, "ce_30d": 0, "ue_30d": 0, "ue_total": 0, "ce_total": 0, "ue_last": "", "ce_last": "",
            "disabled_30d": 0, "limit_30d": 0, "new_today": 0, "time_invalid": 0, "vendor_ue_30d": 0}
    dimms = {}
    ce_day_dimms = defaultdict(set)   # day -> DIMM keys with CE (H14)
    for e in rows:
        if e["date"] > day:
            continue
        kind, a = e["kind"], age(e["date"])
        valid = e.get("time_valid", "1") != "0"
        if not valid:
            node["time_invalid"] += 1
        if e["first_seen"] == day and kind in ("ue", "disabled", "ce"):
            node["new_today"] += 1
        if kind == "limit":
            if valid and 0 <= a <= 30:
                node["limit_30d"] += 1
            continue
        k = dimm_key(e)
        x = dimms.get(k)
        if x is None:
            d = by_sn.get(e["sn"]) if e.get("sn") else None
            x = dimms[k] = {"key": k, "slot": e["text_loc"] if e.get("text_loc") and not e["text_loc"].startswith("#") else "",
                            "sensor": ("#" + e["sensor"]) if e.get("sensor") and not e.get("text_loc") else "",
                            "sn": e.get("sn", ""), "part": e.get("part", ""), "size": e.get("size", ""), "vendor": e.get("vendor", ""),
                            "dmi_slot": (e["slot"] + (" / " + e["bank"] if e.get("bank") and "CHANNEL" in e["bank"].upper() else "")) if e.get("sn") else "",
                            "bank": e.get("bank", ""), "dmi": {"slot": e.get("slot", ""), "bank": e.get("bank", "")} if e.get("sn") else None,
                            "speed": (d or {}).get("speed", ""), "via": set(), "sources": set(),
                            "ce_30d": 0, "ce_7d": 0, "ce_prev7d": 0, "ce_total": 0, "ue_30d": 0, "ue_total": 0, "disabled_30d": 0,
                            "ce_times": [], "ce_days14": set(), "first": e["date"], "last": e["date"], "first_ce": "", "time_invalid": 0}
        x["first"], x["last"] = min(x["first"], e["date"]), max(x["last"], e["date"])
        if e.get("via"):
            x["via"].add(e["via"])
        x["sources"].add(e.get("source") or "sel")
        if not valid:
            x["time_invalid"] += 1
        if kind == "ue":
            node["ue_total"] += 1
            x["ue_total"] += 1
            if valid:
                node["ue_last"] = max(node["ue_last"], e["date"])
                if 0 <= a <= 30:
                    node["ue_30d"] += 1
                    x["ue_30d"] += 1
                    if e.get("source") == "redfish":
                        node["vendor_ue_30d"] += 1
        elif kind == "ce":
            node["ce_total"] += 1
            x["ce_total"] += 1
            if not x["first_ce"] or e["date"] < x["first_ce"]:
                x["first_ce"] = e["date"]
            if valid:
                node["ce_last"] = max(node["ce_last"], e["date"])
                if 0 <= a <= 30:
                    node["ce_30d"] += 1
                    x["ce_30d"] += 1
                if 0 <= a <= 7:
                    node["ce_7d"] += 1
                    x["ce_7d"] += 1
                    ce_day_dimms[e["date"]].add(k)
                elif 7 < a <= 14:
                    x["ce_prev7d"] += 1
                if 0 <= a <= 14:
                    x["ce_days14"].add(e["date"])
                try:
                    t = dt.datetime.fromisoformat("%sT%s" % (e["date"], e["time"])).replace(tzinfo=TZ)
                    if -86400 <= (ref - t).total_seconds() <= 7 * 86400:  # BMC clocks may run ahead
                        x["ce_times"].append(t)
                except ValueError:
                    pass
        elif kind == "disabled":
            if valid and 0 <= a <= 30:
                node["disabled_30d"] += 1
                x["disabled_30d"] += 1
    m.update(node)

    hits = []

    def hit(code):
        if code not in hits:
            hits.append(code)

    # ---------- DIMM-level health
    h14 = False
    for dday, keys in ce_day_dimms.items():
        cpus = {cpu_of(dimms[k]["dmi"], dimms[k]["slot"]) for k in keys}
        if len(keys) >= H14_DIMMS and len(cpus - {None}) >= 2:
            h14 = True
    chan = defaultdict(set)
    for k, x in dimms.items():
        if x["ce_7d"] and x["dmi"]:
            c = channel_of(x["dmi"])
            if c:
                chan[c].add(k)
    rf = ctx["redfish"].get(ip) if ctx["redfish"] else None
    rf_bad = {}
    if rf:
        for d in rf.get("dimms", []):
            if d.get("health") and d["health"] != "OK" and d.get("state", "Enabled") == "Enabled":
                rf_bad[d.get("sn") or d.get("loc")] = d
    for k, x in dimms.items():
        x["hits"] = []
        old = replaced_old.get(x["sn"]) if x["sn"] else None
        if old and old.get("date") and old["date"] <= day:
            # pulled DIMM: out of the risk set after the replacement day (plan v1.2 2.3, intervention censoring)
            x["replaced"] = {"date": old["date"], "reason": old.get("reason", ""), "new_sn": old.get("new_sn", ""),
                             "censored": not (x["ue_total"] or x["disabled_30d"]) or old.get("reason") == "ce_storm"}
            continue
        ts = sorted(x["ce_times"])
        j, best = 0, 0
        for i in range(len(ts)):
            while (ts[i] - ts[j]).total_seconds() > 86400:
                j += 1
            best = max(best, i - j + 1)
        x["ce_24h_max"] = best
        if best >= H4_24H or x["ce_7d"] >= H4_7D:
            x["hits"].append("mem_h4_ce_storm")
        if len(x["ce_days14"]) >= H6_DAYS:
            x["hits"].append("mem_h6_ce_persistent")
        if x["dmi"] and channel_of(x["dmi"]) and len(chan.get(channel_of(x["dmi"]), ())) >= 2:
            x["hits"].append("mem_h7_channel_multi")
        new = replaced_new.get(x["sn"]) if x["sn"] else None
        if new and new.get("date") and 0 <= age(new["date"]) <= 30 and x["ce_30d"] and x["last"] >= new["date"]:
            x["hits"].append("mem_h8_ce_after_swap")
        if x["ce_7d"] > 2 * x["ce_prev7d"] and x["ce_7d"] >= H10_MIN and not clears:
            x["hits"].append("mem_h10_ce_rising")
        if x["sn"] and x["sn"] in rf_bad:
            x["hits"].append("mem_h15_bmc_health")
        if h14:
            x["suppressed"] = [h for h in x["hits"] if h in ("mem_h4_ce_storm", "mem_h6_ce_persistent", "mem_h7_channel_multi")]
            x["hits"] = [h for h in x["hits"] if h not in x["suppressed"]]
        for h in x["hits"]:
            hit(h)
    for key, d in rf_bad.items():  # Redfish says a DIMM is unhealthy but the ledger has no record for it
        if not any(x["sn"] == key for x in dimms.values()):
            dd = by_sn.get(key) or {}
            dimms["sn:" + key] = {"key": "sn:" + key, "slot": d.get("loc", ""), "sensor": "", "sn": key, "part": dd.get("part", ""),
                                  "size": dd.get("size", ""), "vendor": dd.get("vendor", ""), "dmi_slot": dd.get("slot", d.get("loc", "")),
                                  "bank": dd.get("bank", ""), "dmi": dd or None, "speed": dd.get("speed", ""), "via": {"redfish"},
                                  "sources": {"redfish"}, "ce_30d": 0, "ce_7d": 0, "ce_prev7d": 0, "ce_total": 0, "ue_30d": 0,
                                  "ue_total": 0, "disabled_30d": 0, "first": day, "last": day, "first_ce": "", "time_invalid": 0,
                                  "hits": ["mem_h15_bmc_health"], "bmc_health": d.get("health")}
            hit("mem_h15_bmc_health")
    if h14:
        hit("mem_h14_platform_ce")

    # ---------- node-level health
    if m["ue_30d"]: hit("mem_h1_ue")
    if m["disabled_30d"]: hit("mem_h2_disabled")
    if (m["hw_corrupted_kb"] or 0) > 0 or m["mem_offline"]: hit("mem_h3_bad_page")
    if m["limit_30d"]: hit("mem_h5_ce_limit")
    if m["edac"]: hit("mem_h9_edac")
    bmc_alerts = ctx["bmc_sensors"][0].get(ip, []) if ctx["bmc_sensors"] else []
    if bmc_alerts and not (ctx["platform_of"](base) or {}).get("redfish") == "ue_replaced":
        hit("mem_h15_bmc_health")
        m["bmc_dimm_sensors"] = bmc_alerts
    if ctx["bmc_sensors"]:
        if ctx["bmc_sensors"][2].get(ip):
            m.setdefault("notes", []).append("bmc_ecc_sensor_sticky:" + ",".join(ctx["bmc_sensors"][2][ip]))
        if ctx["bmc_sensors"][1].get(ip):
            m.setdefault("quality", []).append("bmc_sensor_mapping_unstable")
    if m["ue_total"] and not m["ue_30d"]: hit("mem_h12_ue_history")
    if m["mce"] and not m["edac"]: hit("mem_h13_mce_undecoded")
    if m["ce_30d"] and not any(RULE_LEVEL[h] in ("warning", "critical") for h in hits if h.startswith("mem_h")):
        hit("mem_h11_ce")

    # ---------- presence (MEM-S1..S4)
    hist = ctx["history"].get(ip, [])
    presence = []
    if dimm_rec:
        mx = max([_int(h["dimms"]) or 0 for h in hist] + [0])
        prev_slots = {(d["slot"], d.get("bank", "")) for d in ctx["prev_dimms"].get(ip, [])}
        now_slots = {(d["slot"], d.get("bank", "")) for d in dimm_rec}
        gone = sorted(" / ".join(x for x in s if x) for s in prev_slots - now_slots)
        if len(dimm_rec) < mx or gone:
            hit("mem_s1_dimm_missing")
            presence.append({"code": "mem_s1_dimm_missing", "max_14d": mx, "now": len(dimm_rec), "slots_gone": gone})
        mt = base.get("memtotal_kb")
        mts_hist = [_int(h.get("memtotal_kb")) for h in hist if _int(h.get("memtotal_kb"))]
        smallest = min([size_gb(d["size"]) or 0 for d in dimm_rec if size_gb(d["size"])] or [0])
        if mt and mts_hist and smallest and max(mts_hist) - mt >= smallest * 1024 * 1024 * 0.9:
            hit("mem_s3_capacity_drop")
            presence.append({"code": "mem_s3_capacity_drop", "max_14d_kb": max(mts_hist), "now_kb": mt})
        sizes = Counter(d["size"] for d in dimm_rec)
        rated = Counter(d.get("speed", "") for d in dimm_rec)
        slow = [d["slot"] for d in dimm_rec if mts(d.get("cfg_speed")) and mts(d.get("speed")) and mts(d["cfg_speed"]) < mts(d["speed"])]
        lenovo_order = rf and any("Unsupported Dimm Population" in (e.get("message") or "") for e in rf.get("events", []))
        if len(sizes) > 1 or len(rated) > 1 or slow or lenovo_order:
            hit("mem_s4_config")
            presence.append({"code": "mem_s4_config", "sizes": dict(sizes), "rated_speeds": dict(rated),
                             "below_rated": slow[:8], "below_rated_n": len(slow), "unsupported_population": bool(lenovo_order)})
    swaps = [r for r in repl if r.get("date") and 0 <= age(r["date"]) <= 30]
    if swaps:
        presence.append({"code": "mem_s2_replaced", "n_30d": len(swaps)})
    m["presence"] = presence

    # ---------- observability (MEM-O1..O7)
    pf = ctx["platform_of"](base)
    flags = base.get("flags", "")
    codes = []
    # sel_incomplete / sel_list_error (collector v2): the listing stopped short of sel info Entries. 11-27: on GOOXI
    # 174.225/245 with a full SEL both ipmitool and ipmi_exporter saw 0 CE while ~1364 CE records were stored.
    if re.search(r"(^|,)(sel_read_error|sel_no_output|no_ipmitool|no_ipmi_dev|cmd_timeout:sel_elist|sel_incomplete:[^,]*|sel_list_error)(,|$)", flags):
        codes.append("mem_o5_sel_unreadable")
    if "sel_full" in flags:
        codes.append("mem_o1_sel_full")
    if not pf or not pf["ce_source"].startswith("sel"):
        codes.append("mem_o2_platform_no_ce")
    problem_unmapped = [x for x in dimms.values() if not x["sn"] and (x["ue_total"] or x["disabled_30d"] or x["ce_30d"])]
    if problem_unmapped and not rf_bad:
        codes.append("mem_o3_slot_unresolved")
    if m["time_invalid"]:
        codes.append("mem_o4_bmc_time_invalid")
    if clears:
        codes.append("mem_o6_sel_cleared")
    if pf and pf["redfish"] in REDFISH_PLATFORMS and ctx["redfish"] is not None and (not rf or rf.get("status") != "ok"):
        codes.append("mem_o7_redfish_unreadable")
    level = "blind" if "mem_o5_sel_unreadable" in codes else \
        "limited" if any(c in codes for c in ("mem_o1_sel_full", "mem_o2_platform_no_ce", "mem_o3_slot_unresolved", "mem_o7_redfish_unreadable")) else "full"
    m["observability"] = {"level": level, "codes": codes,
                          "platform": pf["platform"] if pf else "", "ce_source": pf["ce_source"] if pf else "unlisted",
                          "ceiling": pf["ceiling"] if pf else "L1"}
    if clears:
        m["observability"]["sel_clears_7d"] = [{"time": c["time"], "by": c["by"], "pre": c["pre"], "post": c["post"]} for c in clears]

    # ---------- DIMM issues (R-1: one row per SN; unresolved rows by slot text / sensor, mapped=false)
    issues = []
    for x in dimms.values():
        row = {k: x[k] for k in ("slot", "sensor", "sn", "part", "size", "vendor", "speed", "ce_30d", "ce_7d", "ce_total",
                                 "ue_30d", "ue_total", "disabled_30d", "first", "last", "first_ce")}
        if not row["sensor"]:
            row.pop("sensor")
        row["dmi_slot"] = x["dmi_slot"]
        row["mapped"] = bool(x["sn"])
        row["via"] = ",".join(sorted(x["via"]))
        row["sources"] = ",".join(sorted(x["sources"]))
        row["ce_24h_max"] = x.get("ce_24h_max", 0)
        row["ce_days_14d"] = len(x.get("ce_days14", ()))
        row["hits"] = x.get("hits", [])
        for extra in ("replaced", "suppressed", "bmc_health"):
            if x.get(extra):
                row[extra] = x[extra]
        if x["time_invalid"]:
            row["time_invalid"] = x["time_invalid"]
        issues.append(row)
    issues.sort(key=lambda e: (-e["ue_30d"], -e["disabled_30d"], -len(e["hits"]), -e["ue_total"], -e["ce_30d"], -e["ce_total"], e.get("dmi_slot") or e["slot"]))
    m["dimm_issues"] = issues[:32]
    m["dimm_warnings"] = sum(1 for e in issues if any(RULE_LEVEL.get(h) == "warning" for h in e["hits"]))
    m["locs"] = [e["dmi_slot"] or e["slot"] for e in issues[:6] if e["dmi_slot"] or e["slot"]]
    m["hits"] = sorted(hits, key=lambda h: (-LEVEL_RANK[RULE_LEVEL[h]], h))
    m["level"] = worst([RULE_LEVEL[h] for h in hits])
    return m
