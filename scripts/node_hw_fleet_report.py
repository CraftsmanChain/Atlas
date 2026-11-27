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
        warning  : SEL correctable ECC >=100 in last 7d or 'logging limit reached' in 30d; kernel EDAC CE/UE lines
        attention: any SEL correctable ECC in last 30d; SEL uncorrectable ECC older than 30d;
                   kernel 'Machine check events logged' (undecoded MCE, no EDAC error line)
  v1.1 (2026-11-24): kernel EDAC/MCE counts error lines only (driver-init lines ignored); per-slot memory
                   issues mapped to dmidecode slot/SN/part ("dimm_issues"); unmonitored nodes moved to excluded.
  v1.2 (2026-11-24): memory windows counted from the cross-day ledger (node_hw_ledger.py); DIMM slot decoded from raw
                   SEL event data per board (selraw.tsv/board.tsv); disk 'slot'; snapshot gains 'ledger' + 'events'.
  life (SSD only): remaining_days = power_on_hours*(100-wear)/wear/24 at the lifetime-average wear rate (wear>=1).
  v2.0 (2026-11-27, memory plan v1.2): memory rules replaced by node-hw-rules-v2.0 (scripts/node_hw_mem_rules.py:
                   presence MEM-S1..S4, observability MEM-O1..O7 with the visibility matrix configs/node-hw-mem-platforms.tsv,
                   health MEM-H1..H15; hit codes mem_*); dimm_issues one row per SN (R-1); SEL rows parsed by
                   node_hw_ledger.parse_sel_row (invalid BMC time kept, out of windows; unparsed rows -> sel_unparsed.tsv);
                   BMC counter reconciliation mem_bmc_recon.tsv (R-4); SEL clears (R-5); DIMM replacements (R-8);
                   Redfish poll redfish-mem.json; mem_state.tsv history; mem-report.md for the daily report. Disk rules
                   unchanged (rule_versions.disk = node-hw-rules-v1.2).
"""
import csv, datetime as dt, glob, gzip, json, os, re, sys, urllib.parse, urllib.request
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import node_hw_ledger as ledger  # noqa: E402
import node_hw_mem_rules as R  # noqa: E402
from collections import Counter, defaultdict

RULES = R.RULES                  # memory node-hw-rules-v2.0 (plan v1.2); disk rules unchanged since v1.2
DISK_RULES = "node-hw-rules-v1.2"
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
    if os.environ.get("ATLAS_HW_VM_OFF") == "1":
        return None
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


# Kernel lines that the KERNCNT pattern matches but are driver init / informational, not errors
# (2026-11-24: boot-time "EDAC MC0: Giving out device", "MCE: In-kernel MCE decoding enabled" made
# kernel_edac_mce fire on freshly rebooted nodes).
KERN_NOISE = re.compile(r"EDAC MC: Ver|Giving out device|detected \(node|chip selects|EDAC amd64: MC: |MCT channel count|"
                        r"decoding enabled|ECC (is )?(enabled|disabled)|registered|using x\d+ syndromes|EDAC [a-z0-9_]+: (Ver|F\w+h)|"
                        r"Enabled MCE|mce: CPU\d+: (Core|Package) temperature|mce: \[Hardware Error\]: CPU \d+: Machine Check: 0 Bank|EDAC .*: Node \d+: DRAM ECC")
KERN_EDAC = re.compile(r"EDAC.*(CE|UE|error|Error)|(Corrected|Uncorrected) error")
KERN_MCE = re.compile(r"mce: \[Hardware Error\]|Machine check|Machine Check")


def kern_errors(text):
    """Error-only EDAC / MCE line counts from ##KERNLINES (tail 300 lines, so a lower bound)."""
    edac = mce = 0
    for line in section(text, "KERNLINES").splitlines():
        if KERN_NOISE.search(line):
            continue
        if "EDAC" in line and KERN_EDAC.search(line):
            edac += 1
        elif KERN_MCE.search(line):
            mce += 1
    return edac, mce


def sel_extra_locs(text):
    """SEL rows whose slot sits in the 7th ipmitool column ('... | Asserted | CPU 1 DIMM 0'); sel.tsv drops it."""
    out = {}
    for line in section(text, "SEL").splitlines():
        p = [x.strip() for x in line.split("|")]
        if len(p) >= 7 and p[3].startswith("Memory") and p[6]:
            out[p[0].lstrip()] = p[6]
    return out


def slot_key(name):
    """(cpu, slot) from SEL/dmidecode slot names, e.g. DIMMH2(CPU1) / P1-DIMMH2 -> ('1','H2');
    CPU2_DIMM_B5 / 'Processor2 Ch5 DIMM B5' -> ('2','B5'). None when the name has no CPU part."""
    n = name.strip().upper()
    for rx in (r"^DIMM([A-Z]\d+)\(CPU(\d+)\)$", r"^CPU(\d+)_DIMM_([A-Z]\d+)$", r"^P(\d+)-DIMM([A-Z]\d+)$",
               r"^PROCESSOR(\d+) CH\d+ DIMM ([A-Z]\d+)$"):
        m = re.match(rx, n)
        if m:
            a, b = m.groups()
            return (b, a) if rx.startswith("^DIMM(") else (a, b)
    return None


def decode_sel_slot(board, sensor, ed):
    """Slot from raw memory SEL event data (data2/data3), per board family. Verified 2026-11-24 on 96 nodes
    (Atlas-Docs evidence/hw-sel-decode-2026-11-24): Supermicro H12 189/189 equal to the BMC text; SuperCloud
    R8428 / ASUS KMPG 9257/9257 decode to a populated slot with CPU = sensor (d1/d3); GOOXI G3DCTR 5145/5145
    CPU equal to the BMC text 'CPU n DIMM m'. Returns (cpu, channel_letter, slot_index_in_channel, style) or None."""
    try:
        d1, d2, d3 = int(ed[0:2], 16), int(ed[2:4], 16), int(ed[4:6], 16)
    except (ValueError, IndexError):
        return None
    b = board.upper()
    if "H12DSG" in b and d2 >> 4 and (d2 & 15) in (0xA, 0xB):          # DIMM<ch><1|2>(CPU<n>)
        return ((d3 & 1) + 1, chr(64 + (d2 >> 4)), (d2 & 15) - 9, "smc")
    if ("R8428" in b or "KMPG" in b) and d2 == 0xFF:                    # DIMM_<A..P><n>, CPU1 = A..H
        cpu = (d3 >> 5) & 1
        return (cpu, chr(65 + ((d3 >> 2) & 7) + 8 * cpu), (d3 & 3) + 1, "letter")
    if "G3DCTR" in b and d3 <= 1:                                       # DIMM <m> / P<cpu> CHANNEL <X>
        return (d3, chr(65 + (d2 >> 4)), d2 & 15, "goo")
    return None


def dimm_for_decoded(dec, dimms):
    cpu, ch, idx, style = dec
    if style == "smc":
        want = ["P%d-DIMM%s%d" % (cpu, ch, idx), "DIMM%s%d" % (ch, idx)]
        hits = [d for d in dimms if d["slot"].upper() in want]
    elif style == "letter":
        hits = [d for d in dimms if d["slot"].upper() in ("DIMM_%s%d" % (ch, idx), "DIMM%s%d" % (ch, idx))]
    else:
        hits = [d for d in dimms if d["slot"].upper() == "DIMM %d" % idx and d.get("bank", "").upper() == "P%d CHANNEL %s" % (cpu, ch)]
    return hits[0] if len(hits) == 1 else None


def match_dimm(loc, dimms):
    """Unique dmidecode record for a SEL slot name, else None (ambiguous or unmappable, e.g. '#0x08')."""
    if not loc or loc.startswith("#"):
        return None
    u = loc.strip().upper()
    hits = [d for d in dimms if d["slot"].upper() == u or d["slot"].upper().startswith(u + " ")]
    if not hits:
        k = slot_key(loc)
        if k:
            hits = [d for d in dimms if slot_key(d["slot"]) == k]
    return hits[0] if len(hits) == 1 else None


def resolve_slot(ip, sel_id, text_loc, board, selraw, dimms):
    """Per SEL record: {'key','slot','sensor','dimm','via'}; raw decode first, then BMC text, then sensor number."""
    raw = selraw.get((ip, sel_id.lower().lstrip("0")))
    if raw:
        dec = decode_sel_slot(board, raw[0], raw[1])
        if dec:
            d = dimm_for_decoded(dec, dimms)
            if d:
                return {"key": d["slot"] + "|" + d.get("bank", ""), "slot": text_loc if text_loc and not text_loc.startswith("#") else "", "dimm": d, "via": "sel_raw"}
    if text_loc and not text_loc.startswith("#"):
        d = match_dimm(text_loc, dimms)
        return {"key": (d["slot"] + "|" + d.get("bank", "")) if d else text_loc, "slot": text_loc, "dimm": d, "via": "sel_text" if d else ""}
    sensor = text_loc or ("#0x" + raw[0] if raw else "")
    return {"key": sensor or "#unknown", "slot": "", "sensor": sensor, "dimm": None, "via": ""}


def collected_ref(day_dir, day):
    """Collection reference time (Asia/Shanghai): nodes.tsv mtime, else <day> 01:40."""
    try:
        t = dt.datetime.fromtimestamp(os.path.getmtime(os.path.join(day_dir, "nodes.tsv")), R.TZ)
        if t.date().isoformat() == day:
            return t
    except OSError:
        pass
    return dt.datetime.fromisoformat(day + "T01:40:00").replace(tzinfo=R.TZ)


def read_dimms(path):
    out = defaultdict(list)
    for r in rows(path):
        if len(r) >= 3:
            r = r + [""] * (10 - len(r))
            out[r[0]].append({"slot": r[1].strip(), "size": r[2].strip(), "vendor": r[3].strip(), "sn": r[4].strip(),
                              "part": r[5].strip(), "speed": r[6].strip(), "bank": r[7].strip(), "cfg_speed": r[8].strip(),
                              "rank": r[9].strip()})
    return out


def read_rt_events(root, ref, hours=48):
    """MEM-RT events (internal/nodehw memrt, mem_rt_events.tsv) of the last <hours> before ref, for the report."""
    p = os.path.join(root, "mem_rt_events.tsv")
    out = []
    if not os.path.exists(p):
        return out
    lo = (ref - dt.timedelta(hours=hours)).strftime("%Y-%m-%d %H:%M:%S")
    hi = ref.strftime("%Y-%m-%d %H:%M:%S")
    for r in rows(p, True):
        if lo <= r.get("time", "") <= hi:
            out.append(r)
    return out[-500:]


def worst(levels):
    return max(levels, key=lambda x: LEVEL_RANK[x]) if levels else "clear"


def replacement_summary(repl, memory):
    """Plan v1.2 2.3: decode accuracy = sn_match yes / (yes + no) per platform; basis=atlas_page counted separately
    (adoption, not accuracy evidence); CE-triggered swaps are intervention censoring, not positives."""
    plat = {m["ip"]: m["observability"]["platform"] or m["board"] for m in memory}
    acc = defaultdict(Counter)
    for r in repl:
        acc[plat.get(r["ip"], "?")][r.get("sn_match") or "unknown"] += 1
    return {"total": len(repl), "by_source": dict(Counter(r.get("source", "") for r in repl)),
            "by_reason": dict(Counter(r.get("reason") or "unregistered" for r in repl)),
            "atlas_page": sum(1 for r in repl if r.get("basis") == "atlas_page"),
            "censored_ce_storm": sum(1 for r in repl if r.get("reason") == "ce_storm"),
            "decode_accuracy": {p: {"yes": c["yes"], "no": c["no"], "unknown": c["unknown"],
                                    "rate": round(c["yes"] / (c["yes"] + c["no"]), 3) if c["yes"] + c["no"] else None} for p, c in acc.items()}}


def write_mem_report(day_dir, report, extra):
    """Memory section of the daily report (appended to summary.md by the collector as daily-latest.md)."""
    ms = report["summary"]["memory"]
    L = ["", "## 内存（规则 %s，可见性矩阵 %s）" % (report["rule_versions"]["memory"], ms["platform_matrix"]), ""]
    o = ms["observability"]
    L.append("- 节点 %d：严重 %d / 警告 %d / 关注 %d；可观测性 完整 %d / 受限 %d / 不可见 %d；CE 可见平台 %d 台（%.0f%%）、%d 根条" % (
        ms["nodes"], ms["by_level"]["critical"], ms["by_level"]["warning"], ms["by_level"]["attention"], o["full"], o["limited"], o["blind"],
        ms["ce_visible_nodes"], 100 * (ms["ce_visible_ratio"] or 0), ms["ce_visible_dimms"]))
    q = ms["quality"]
    L.append("- 告警质量：条级警告 %d 根（较上一快照新增 %d，7 天新增 %d，目标 ≤5/周）；节点级警告+严重 %d 台（新增 %d，7 天新增 %d，目标 ≤10/周）" % (
        q["dimm_warnings"], q["new_dimm_warnings"], q["new_dimm_warnings_7d"], q["node_warnings"], q["new_node_warnings"], q["new_node_warnings_7d"]))
    rc = ms["recon"]
    if rc is None:
        L.append("- BMC 计数对账：VM 不可用，未做")
    else:
        L.append("- BMC 计数对账（ipmi_exporter，CE = correctable − uncorrectable，取采集时刻前 3 小时内最后一个值）：%d 台，一致 %d，不一致 %d，BMC 侧无数据 %d" % (
            rc["compared"], rc["ok"], rc["mismatch"], rc.get("no_bmc_series", 0)))
        for x in (report.get("memory_recon") or [])[:30]:
            L.append("  - %s BMC CE/UE %s/%s，台账 CE/UE %s/%s（limit %s）原因：%s" % (x["ip"], x["bmc_ce"], x["bmc_ue"], x["atlas_ce"], x["atlas_ue"], x["atlas_limit"], x["reasons"] or "未知"))
    cl = [c for c in extra["clears"] if c["time"][:10] >= (dt.date.fromisoformat(report["day"]) - dt.timedelta(days=7)).isoformat()]
    L.append("- SEL 被清（近 7 天）：%d 次（Atlas %d / 外部 %d）" % (len(cl), sum(1 for c in cl if c["by"] == "atlas"), sum(1 for c in cl if c["by"] != "atlas")))
    for c in cl[-20:]:
        L.append("  - %s %s by=%s 来源 %s %s→%s" % (c["time"], c["ip"], c["by"], c["source"], c["pre"], c["post"]))
    if extra["unparsed"]:
        L.append("- 未能解析的内存 SEL 记录：%d 台（见 sel_unparsed.tsv）：%s" % (len(extra["unparsed"]), "；".join(
            "%s %s" % (ip, ",".join("%s=%d" % kv for kv in c.items())) for ip, c in sorted(extra["unparsed"].items())[:15])))
    rp = ms["replacements"]
    L.append("- 换条：累计 %d（来源 %s；原因 %s）；页面采纳 %d；CE 触发（删失）%d" % (rp["total"], rp["by_source"], rp["by_reason"], rp["atlas_page"], rp["censored_ce_storm"]))
    rt = extra["rt_events"]
    L.append("- 近实时事件（48 小时，MEM-RT）：%d 条" % len(rt))
    for e in rt[-15:]:
        L.append("  - %s %s %s %s→%s" % (e.get("time"), e.get("ip"), e.get("signal"), e.get("before"), e.get("after")))
    if ms["redfish"]:
        L.append("- Redfish 日轮询：%d 个 BMC，%s" % (ms["redfish"]["targets"], ms["redfish"]["by_status"]))
    else:
        L.append("- Redfish 日轮询：当日无 redfish-mem.json")
    with open(os.path.join(day_dir, "mem-report.md"), "w") as f:
        f.write("\n".join(L) + "\n")


def main():
    day_dir = sys.argv[1].rstrip("/")
    out_path = sys.argv[2] if len(sys.argv) > 2 else os.path.join(day_dir, "fleet-hw.json")
    day = os.path.basename(day_dir)
    day_date = dt.date.fromisoformat(day)
    prev = prev_day_dir(day_dir)

    nodes = {r[0]: {"kind": r[1], "host": r[2] if len(r) > 2 else ""} for r in rows(os.path.join(day_dir, "nodes.tsv")) if r}
    # Out-of-scope nodes (user decision 2026-11-23): the day's excluded.tsv (written by the collector) plus the
    # current exclude file, so snapshots of days collected before the decision are filtered the same way on re-run.
    excluded = {}
    for r in rows(os.path.join(day_dir, "excluded.tsv")):
        if r and r[0]:
            excluded[r[0]] = {"ip": r[0], "kind": r[1] if len(r) > 1 else "", "host": r[2] if len(r) > 2 else "",
                              "reason": r[3] if len(r) > 3 else ""}
    for r in rows(os.environ.get("ATLAS_HW_EXCLUDE_FILE", "/ops/atlas/configs/node-hw-exclude.tsv")):
        if r and r[0] and not r[0].startswith("#") and r[0] not in excluded and r[0] in nodes:
            excluded[r[0]] = {"ip": r[0], "kind": nodes[r[0]]["kind"], "host": nodes[r[0]]["host"] or (r[2] if len(r) > 2 else ""),
                              "reason": r[1] if len(r) > 1 else ""}
    ledger_stats = {"version": ledger.LEDGER_VERSION}
    mon = monitored_ips()
    # User decision 2026-11-24: nodes absent from monitoring (no node_exporter and no IPMI series in VM)
    # are not shown; they are listed under excluded_nodes with this reason so the count stays auditable.
    # Only uncollected ones are dropped: a node we did collect keeps its disk/memory data.
    st_raw = {r["ip"]: r["status"] for r in rows(os.path.join(day_dir, "status.tsv"), True)}
    if mon is not None:
        for ip, n in list(nodes.items()):
            st0 = st_raw.get(ip, "missing")
            if ip not in excluded and not mon.get(ip) and not (st0 == "ok" or st0.startswith("partial")):
                excluded[ip] = {"ip": ip, "kind": n["kind"], "host": n["host"], "reason": "不在监控（VM 无 node_exporter / IPMI）"}
    for ip in excluded:
        nodes.pop(ip, None)
    status = {r["ip"]: r for r in rows(os.path.join(day_dir, "status.tsv"), True) if r["ip"] not in excluded}
    follow = defaultdict(list)
    for r in rows(os.path.join(day_dir, "followup.tsv"), True):
        follow[r["ip"]].append(r["category"])

    # ---------------- disks ----------------
    smart = [r for r in rows(os.path.join(day_dir, "smart.tsv"), True) if r.get("ip") not in excluded]
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
        m_slot = re.search(r"megaraid,(\d+)", d["dev"])
        d["slot"] = (sc or {}).get("slot") or ("RAID PD %s" % m_slot.group(1) if m_slot else d["dev"].split(" ")[0])
        disks.append(d)
    disk_ledger, disk_new = ledger.update_disk(day, disks)
    ledger_stats["disk_new"] = disk_new

    # ---------------- memory (rules v2, memory plan v1.2) ----------------
    # Every memory SEL record seen today is resolved to a DIMM (raw event data > BMC text) and added to the
    # cross-day ledger (scripts/node_hw_ledger.py); windows are counted from the ledger, so a BMC SEL that wraps or a
    # node missing a day does not lose events. Rules: scripts/node_hw_mem_rules.py (node-hw-rules-v2.0).
    ref = collected_ref(day_dir, day)
    kern = defaultdict(Counter)
    kern_rows = []
    for r in rows(os.path.join(day_dir, "kern.tsv")):
        if len(r) >= 4:
            kern[r[0]][r[2]] += num(r[3]) or 0
            kern_rows.append((r[0], r[1], r[2], r[3]))
    dimm_rec = read_dimms(os.path.join(day_dir, "dimm.tsv"))
    prev_dimm = read_dimms(os.path.join(prev, "dimm.tsv")) if prev else {}
    board = {r[0]: " ".join(r[1:3]) for r in rows(os.path.join(day_dir, "board.tsv")) if r}
    selraw = {(r[0], r[1].strip().lower().lstrip("0")): (r[2].strip().lower(), r[3].strip().lower())
              for r in rows(os.path.join(day_dir, "selraw.tsv")) if len(r) >= 4}
    extra_cache = {}
    today_recs = []
    unparsed = defaultdict(Counter)   # R-2/R-3 diagnostics: memory-like SEL rows the parser could not use
    unparsed_samples = []
    atlas_sel = defaultdict(Counter)  # R-4: memory SEL records in the SEL right now, any direction / time
    for r in rows(os.path.join(day_dir, "sel.tsv")):
        if len(r) < 6 or r[0] in excluded:
            continue
        r = r + [""] * (7 - len(r))
        ip, sel_id = r[0], r[1].strip()
        k0 = ledger.sel_kind(r[5]) if (r[4].startswith("Memory") or re.search(r"ECC|Memory Device Disabled", r[5], re.I)) else None
        if k0:
            atlas_sel[ip][k0] += 1
            if r[6].strip() == "Deasserted":
                atlas_sel[ip]["deasserted"] += 1
        rec, why = ledger.parse_sel_row(sel_id, r[2], r[3], r[4], r[5], r[6], day)
        if rec is None:
            if why != "not_memory":
                key = why.split(":")[0]
                unparsed[ip][key] += 1
                if key != "deasserted" and len(unparsed_samples) < 200:
                    unparsed_samples.append([ip, sel_id, r[2], r[3], r[4], r[5], r[6]])
            continue
        if rec["time_valid"] == "0":
            unparsed[ip]["time_invalid"] += 1
        ev, kind = r[5], rec["kind"]
        loc = re.search(r"@([^)]*\))", ev) or re.search(r"Memory (\S+)$", r[4].strip())
        name = loc.group(1) if loc else ""
        if not name:
            if ip not in extra_cache:
                extra_cache[ip] = sel_extra_locs(node_text(day_dir, ip))
            name = extra_cache[ip].get(sel_id, "")
        raw = selraw.get((ip, sel_id.lower().lstrip("0")))
        rs = resolve_slot(ip, sel_id, name, board.get(ip, ""), selraw, dimm_rec.get(ip, [])) if kind != "limit" else {"dimm": None}
        d = rs.get("dimm") or {}
        today_recs.append({"ip": ip, "sel_id": sel_id, "date": rec["date"], "time": rec["time"], "kind": kind,
                           "text_loc": name, "sensor": ("0x" + raw[0]) if raw else "", "raw": raw[1] if raw else "",
                           "slot": d.get("slot", ""), "bank": d.get("bank", ""), "sn": d.get("sn", ""), "part": d.get("part", ""),
                           "size": d.get("size", ""), "vendor": d.get("vendor", ""), "via": rs.get("via", "") if d else "",
                           "bmc_date": rec["bmc_date"], "time_valid": rec["time_valid"], "source": "sel"})
    rf = R.load_redfish(day_dir)
    rf_recs = [x for x in R.redfish_ledger_records(rf) if x["ip"] not in excluded]
    for x in rf_recs:  # vendor-log events: resolve the DIMM by SN from today's dmidecode
        d = next((y for y in dimm_rec.get(x["ip"], []) if x["sn"] and y["sn"] == x["sn"]), None)
        if d:
            x.update({"slot": d["slot"], "bank": d.get("bank", ""), "part": d["part"], "size": d["size"], "vendor": d["vendor"]})
    ledger_rows, ledger_new = ledger.update_mem(day, today_recs + rf_recs)
    ledger_stats["mem_new"] = ledger_new
    ledger_stats["mem_total"] = len(ledger_rows)
    kern_ledger, kern_new = ledger.update_kern(day, kern_rows, day_date.year)
    ledger_stats["kern_new"] = kern_new
    ledger_by_ip = defaultdict(list)
    for e in ledger_rows:
        if e["ip"] not in excluded:
            ledger_by_ip[e["ip"]].append(e)

    # replacements (R-8): dmidecode SN change (P-2) + iBMC MemoryReplaced + ops registrations
    auto = []
    for ip, recs in dimm_rec.items():
        if ip in prev_dimm and ip not in excluded:
            auto += R.dmidecode_swaps(ip, recs, prev_dimm[ip], day, ledger._valid_sn)
    auto += [x for x in R.ibmc_swaps(rf) if x["ip"] not in excluded]
    repl_rows, repl_new = ledger.update_replacements(day, auto)
    ledger_stats["replacements_new"] = repl_new
    repl_by_ip = defaultdict(list)
    for x in repl_rows:
        if x.get("date", "") <= day:
            repl_by_ip[x["ip"]].append(x)

    # SEL clears (R-5 / MEM-O6)
    today_si, prev_si = R.selinfo_rows(day_dir), (R.selinfo_rows(prev) if prev else {})
    vm_ev = R.vm_clears(ref - dt.timedelta(days=1), ref) if os.environ.get("ATLAS_HW_VM_OFF") != "1" else None
    if vm_ev is not None:
        vm_ev = [v for v in vm_ev if v["ip"] in nodes]   # in-scope nodes only (excluded already dropped from nodes)
    resets = R.atlas_resets(ledger.root())
    clear_rows, clear_new = ledger.update_clears(day, R.detect_clears(day, today_si, prev_si, vm_ev, resets, ref))
    ledger_stats["sel_clears_new"] = clear_new
    clears_by_ip = defaultdict(list)
    for c in clear_rows:
        if c["time"][:10] <= day and c["ip"] not in excluded:
            clears_by_ip[c["ip"]].append(c)
    clears_today = {c["ip"] for c in clear_rows if (ref - dt.datetime.strptime(c["time"], "%Y-%m-%d %H:%M:%S").replace(tzinfo=R.TZ)).total_seconds() <= 86400
                    and c["time"][:10] <= day}

    # VictoriaMetrics side inputs (best effort; a VM outage degrades to "unknown", never breaks the report)
    bmc_cnt = sensors = None
    if os.environ.get("ATLAS_HW_VM_OFF") != "1":
        try:
            bmc_cnt = R.bmc_counts(ref)
        except Exception as e:  # noqa: BLE001
            print("vm bmc counters skipped: %s" % e, file=sys.stderr)
        try:
            sensors = R.bmc_dimm_sensors(ref)
        except Exception as e:  # noqa: BLE001
            print("vm dimm sensors skipped: %s" % e, file=sys.stderr)
    matrix, matrix_version = R.load_platforms()
    ctx = {"day": day, "ref": ref, "ledger_by_ip": ledger_by_ip, "repl_by_ip": repl_by_ip, "clears_by_ip": clears_by_ip,
           "redfish": rf, "bmc_sensors": sensors, "history": R.read_history(day_dir), "prev_dimms": prev_dimm,
           "platform_of": lambda b: R.platform_of(matrix, b.get("board", ""), b.get("product", ""))}
    memory, state_rows = [], []
    for ip, st in sorted(status.items()):
        if st["status"] not in ("ok",) and not st["status"].startswith("partial"):
            continue
        full = node_text(day_dir, ip)
        txt = section(full, "MEM")
        meta = section(full, "META")
        hc = re.search(r"HardwareCorrupted:\s+(\d+)", txt)
        mt = re.search(r"MemTotal:\s+(\d+)", txt)
        prod = re.search(r"^product=(.*)$", meta, re.M)
        k = kern.get(ip, Counter())
        if re.search(r"^collector=node-hw-collect-v2", meta, re.M):   # C-3: whole-window error counts
            edac_err, mce_err = k.get("edac_err", 0), k.get("mce_err", 0)
        else:                                                           # older days: 300-line tail (lower bound)
            edac_err, mce_err = kern_errors(full)
        base = {"ip": ip, "host": nodes.get(ip, {}).get("host", ""), "kind": nodes.get(ip, {}).get("kind", ""),
                "board": board.get(ip, ""), "product": prod.group(1).strip() if prod else "", "dimm_rec": dimm_rec.get(ip, []),
                "hw_corrupted_kb": int(hc.group(1)) if hc else None, "memtotal_kb": int(mt.group(1)) if mt else None,
                "edac": edac_err, "mce": mce_err, "mem_offline": k.get("mem_offline", 0), "flags": st.get("flags", "")}
        m = R.evaluate_node(ip, ctx, base)
        if unparsed.get(ip):
            m["sel_unparsed"] = dict(unparsed[ip])
        memory.append(m)
        sizes = [R.size_gb(d["size"]) for d in base["dimm_rec"] if R.size_gb(d["size"])]
        state_rows.append({"ip": ip, "dimms": len(base["dimm_rec"]), "memtotal_kb": base["memtotal_kb"] or "",
                           "hw_corrupted_kb": "" if base["hw_corrupted_kb"] is None else base["hw_corrupted_kb"],
                           "min_dimm_gb": min(sizes) if sizes else ""})
    R.write_state(day_dir, state_rows)
    collected_ips = {m["ip"] for m in memory}
    recon = R.reconcile(bmc_cnt, atlas_sel, unparsed, {ip: s.get("flags", "") for ip, s in status.items()}, clears_today, collected_ips) if bmc_cnt is not None else None
    if recon is not None:
        with open(os.path.join(day_dir, "mem_bmc_recon.tsv"), "w") as f:
            cols = ["ip", "bmc_ce", "bmc_ue", "atlas_ce", "atlas_ue", "atlas_limit", "bmc_ce_1d", "verdict", "reasons"]
            f.write("\t".join(cols) + "\n")
            for x in recon:
                f.write("\t".join(str(x[c]) for c in cols) + "\n")
    with open(os.path.join(day_dir, "sel_unparsed.tsv"), "w") as f:
        f.write("ip\tsel_id\tdate\ttime\tsensor\tevent\tdirection\n")
        for x in unparsed_samples:
            f.write("\t".join(x) + "\n")

    # ledger: memory events for the page, aggregated per (node, DIMM, kind, day), last 30 days, in scope.
    # UE / DIMM disabled always listed; CE only on days where that DIMM logged a CE newly seen in the ledger.
    since = (day_date - dt.timedelta(days=30)).isoformat()
    agg = {}
    for e in ledger_rows:
        if e["ip"] in excluded or e["kind"] == "limit" or not (since <= e["date"] <= day):
            continue
        where = e["sn"] or e["text_loc"] or e["sensor"] or "?"
        k = (e["ip"], where, e["kind"], e["date"])
        a_ = agg.get(k)
        if a_ is None:
            a_ = agg[k] = {"type": "memory", "ip": e["ip"], "host": nodes.get(e["ip"], {}).get("host", ""), "date": e["date"],
                           "time": e["time"] if e.get("time_valid", "1") != "0" else "", "kind": e["kind"], "count": 0, "new": 0,
                           "slot": e["slot"] + (" / " + e["bank"] if e["bank"] and "CHANNEL" in e["bank"].upper() else "") if e["sn"] else (e["text_loc"] or ("#" + e["sensor"] if e["sensor"] else "")),
                           "sn": e["sn"], "part": e["part"], "size": e["size"], "first_seen": e["first_seen"], "mapped": bool(e["sn"]),
                           "source": e.get("source") or "sel"}
            if e.get("time_valid") == "0":
                a_["time_invalid"] = True
        a_["count"] += 1
        if a_["time"] and e.get("time_valid", "1") != "0":
            a_["time"] = min(a_["time"], e["time"])
        a_["first_seen"] = min(a_["first_seen"], e["first_seen"])
        if e["first_seen"] == day:
            a_["new"] += 1
    recent = [v for v in agg.values() if v["kind"] in ("ue", "disabled") or v["new"] > 0]
    for x in repl_rows:
        if x["ip"] not in excluded and since <= x.get("date", "") <= day:
            recent.append({"type": "memory", "ip": x["ip"], "host": nodes.get(x["ip"], {}).get("host", ""), "date": x["date"], "time": "",
                           "kind": "replaced", "count": 1, "new": 0, "slot": x.get("slot", ""), "sn": x["old_sn"], "new_sn": x.get("new_sn", ""),
                           "part": "", "size": "", "first_seen": x["date"], "mapped": True, "source": x.get("source", ""),
                           "reason": x.get("reason", ""), "basis": x.get("basis", ""), "sn_match": x.get("sn_match", "")})
    for c in clear_rows:
        if c["ip"] not in excluded and since <= c["time"][:10] <= day:
            recent.append({"type": "memory", "ip": c["ip"], "host": nodes.get(c["ip"], {}).get("host", ""), "date": c["time"][:10],
                           "time": c["time"][11:], "kind": "sel_clear", "count": 1, "new": 0, "slot": "", "sn": "", "part": "", "size": "",
                           "first_seen": c["first_seen"], "mapped": False, "source": c["source"], "by": c["by"], "before": c["pre"], "after": c["post"]})
    rt_events = read_rt_events(ledger.root(), ref)
    mem_extra = {"matrix_version": matrix_version, "recon": recon, "clears": [c for c in clear_rows if since <= c["time"][:10] <= day and c["ip"] not in excluded],
                 "replacements": [x for x in repl_rows if x["ip"] not in excluded], "rt_events": rt_events,
                 "unparsed": {ip: dict(c) for ip, c in unparsed.items()}, "redfish": None if rf is None else {
                     "targets": len(rf), "by_status": dict(Counter(t.get("status", "?") for t in rf.values()))}}

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
    excl = Counter(e["reason"] for e in excluded.values())
    cov = {"nodes_total": len(node_rows), "collected": len(collected),
           "excluded": sum(excl.values()), "excluded_by_reason": dict(excl.most_common()),
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
    # rule quality (plan v1.1 section 3 / v1.2 R-7): new DIMM-level / node-level warnings vs the previous snapshot
    def warn_keys(mem):
        dk = {(x["ip"], e.get("sn") or e.get("slot") or e.get("sensor", "")) for x in mem for e in x.get("dimm_issues", [])
              if any(R.RULE_LEVEL.get(h) == "warning" for h in e.get("hits", []))}
        nk = {x["ip"] for x in mem if x["level"] in ("warning", "critical")}
        return dk, nk
    dk, nk = warn_keys(memory)
    week_new_d = week_new_n = 0
    prev_dk = prev_nk = None
    for back in sorted((d for d in os.listdir(os.path.dirname(os.path.abspath(day_dir))) if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d)
                        and (day_date - dt.timedelta(days=6)).isoformat() <= d < day), reverse=True):
        pj = os.path.join(os.path.dirname(os.path.abspath(day_dir)), back, "fleet-hw.json")
        try:
            pr = json.load(open(pj))
        except (OSError, ValueError):
            continue
        q = ((pr.get("summary") or {}).get("memory") or {}).get("quality") or {}
        if prev_dk is None:
            if pr.get("rule_version") == RULES:
                prev_dk, prev_nk = warn_keys(pr.get("memory", []))
            else:
                prev_dk, prev_nk = set(), set()
        week_new_d += q.get("new_dimm_warnings", 0) if pr.get("rule_version") == RULES else 0
        week_new_n += q.get("new_node_warnings", 0) if pr.get("rule_version") == RULES else 0
    new_d = len(dk - prev_dk) if prev_dk is not None else len(dk)
    new_n = len(nk - prev_nk) if prev_nk is not None else len(nk)
    obs = Counter(m["observability"]["level"] for m in memory)
    quality = {"dimm_warnings": len(dk), "node_warnings": len(nk), "new_dimm_warnings": new_d, "new_node_warnings": new_n,
               "new_dimm_warnings_7d": week_new_d + new_d, "new_node_warnings_7d": week_new_n + new_n,
               "baseline": "first rules-v2 snapshot" if prev_dk is None or not prev_dk and not prev_nk and week_new_d == 0 else "previous rules-v2 snapshot"}
    vis_nodes = [m for m in memory if m["observability"]["ce_source"].startswith("sel")]
    recon_rows = mem_extra["recon"] or []
    mem_summary = {"nodes": len(memory), "by_level": lv(memory), "by_rule": dict(rulem.most_common()),
                   "dimms": sum(m["dimms"] for m in memory), "quality": quality,
                   "observability": {k: obs.get(k, 0) for k in ("full", "limited", "blind")},
                   "observability_by_code": dict(Counter(c for m in memory for c in m["observability"]["codes"]).most_common()),
                   "ce_visible_nodes": len(vis_nodes), "ce_visible_dimms": sum(m["dimms"] for m in vis_nodes),
                   "ce_visible_ratio": round(len(vis_nodes) / len(memory), 3) if memory else None,
                   "by_platform": {p: {"nodes": n} for p, n in Counter(m["observability"]["platform"] or "未登记平台" for m in memory).most_common()},
                   "platform_matrix": mem_extra["matrix_version"],
                   "recon": None if mem_extra["recon"] is None else {"compared": len(recon_rows), "ok": sum(1 for x in recon_rows if x["verdict"] == "ok"),
                                                                     "mismatch": sum(1 for x in recon_rows if x["verdict"] == "mismatch"),
                                                                     "no_bmc_series": sum(1 for x in recon_rows if x["verdict"] == "no_bmc_series")},
                   "unparsed_nodes": len(mem_extra["unparsed"]), "sel_clears_30d": Counter(c["by"] for c in mem_extra["clears"]),
                   "replacements": replacement_summary(mem_extra["replacements"], memory),
                   "redfish": mem_extra["redfish"], "rt_events_48h": len(mem_extra["rt_events"])}
    report = {"schema": "node-hw-fleet-v1", "rule_version": RULES, "rule_versions": {"memory": RULES, "disk": DISK_RULES}, "day": day,
              "previous_day": os.path.basename(prev) if prev else None,
              "collected_at": started, "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "read_only": True, "actions_executed": False,
              "coverage": cov,
              "summary": {"disk": {"total": len(disks), "by_level": lv(disks), "by_rule": dict(rulec.most_common()),
                                   "by_protocol": dict(Counter(d["proto"] or "unknown" for d in disks)),
                                   "with_delta_baseline": len([d for d in disks if d["serial"] in prev_smart]),
                                   "models": [[m, c] for m, c in models.most_common(15)]},
                          "memory": mem_summary},
              "disks": disks, "memory": memory, "nodes": node_rows, "ledger": ledger_stats,
              "memory_recon": [x for x in recon_rows if x["verdict"] != "ok"] if mem_extra["recon"] is not None else None,
              "sel_clears": mem_extra["clears"][-200:], "dimm_replacements": mem_extra["replacements"][-300:],
              "rt_events": mem_extra["rt_events"],
              "events": sorted(recent + [{"type": "disk", "ip": r["ip"], "host": nodes.get(r["ip"], {}).get("host", ""), "date": r["day"], "time": "",
                                          "kind": r["counter"], "slot": r["slot"], "sn": r["serial"], "part": r["model"], "size": r["cap_gb"],
                                          "before": r["before"], "after": r["after"], "first_seen": r["day"], "mapped": True}
                                         for r in disk_ledger if r["ip"] not in excluded and since <= r["day"] <= day],
                              key=lambda e: (e["date"], e["time"]), reverse=True)[:500],
              "excluded_nodes": sorted(excluded.values(), key=lambda e: [int(p) for p in e["ip"].split(".")])}
    tmp = out_path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(report, f, ensure_ascii=False, separators=(",", ":"))
    os.replace(tmp, out_path)
    write_mem_report(day_dir, report, mem_extra)
    print("fleet-hw %s: disks %d %s; memory nodes %d %s; prev=%s" % (day, len(disks), lv(disks), len(memory), lv(memory), report["previous_day"]))


if __name__ == "__main__":
    main()
