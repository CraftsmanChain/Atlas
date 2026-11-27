#!/usr/bin/env python3
"""Hardware component event ledger (C1, hw-ledger-v1). 2026-11-24. Stdlib only, file based, read-only toward nodes.

Accumulates, across daily collections, the events a single day's snapshot loses (BMC SEL wraps when full, nodes
missing a day). Ledger root: $ATLAS_HW_LEDGER_ROOT (default /data/atlas-research/node-hw-ledger):

  mem_events.tsv   one row per memory SEL record (UE / CE / device disabled / CE logging limit):
                   key ip|sel_id|date|time|kind  - the BMC record id plus its timestamp, so a SEL clear that reuses ids
                   does not merge two records. Columns:
                   ip, sel_id, date, time, kind, text_loc, sensor, raw, slot, bank, sn, part, size, vendor, via, first_seen
                   slot/sn are resolved at first sight (raw SEL event data decode > BMC slot text) and only ever filled
                   in later, never overwritten, so a later DIMM swap does not rewrite history.
  disk_events.tsv  one row per (disk serial, counter, day) where a SMART / RAID error counter grew vs the previous
                   collected day: ip, serial, slot, model, cap_gb, counter, before, after, day
  kern_events.tsv  one row per (ip, kernel-log day, class) for error classes, max count seen: ip, day, class, count, first_seen

Idempotent: re-importing a day adds nothing new; first_seen is the earliest import day.

hw-ledger-v1.1 (2026-11-27, memory plan v1.2):
  mem_events.tsv gains bmc_date (raw BMC date text when the SEL time is invalid: Pre-Init, < 2015, after the
                   collection day; MEM-O4) and time_valid ('0' = invalid; date then holds the first-seen day and the
                   record is kept out of the 7/30-day windows) and source ('sel' / 'redfish').
  sel_clear_events.tsv   SEL clears seen from sel info / VictoriaMetrics ipmi_sel_logs_count (MEM-O6, by=atlas|external)
  dimm_replacements.tsv  generated: dmidecode SN changes (P-2) + iBMC MemoryReplaced + ops rows from
                   dimm_replacements.ops.tsv (human-owned, same columns) merged on (ip, old_sn)
SEL row parsing for the fleet report and the SEL rotation verify step is shared here (parse_sel_row).
"""
import csv, datetime as dt, os, re

LEDGER_VERSION = "hw-ledger-v1.1"
MEM_COLS = ["ip", "sel_id", "date", "time", "kind", "text_loc", "sensor", "raw", "slot", "bank", "sn", "part", "size",
            "vendor", "via", "first_seen", "bmc_date", "time_valid", "source"]
CLEAR_COLS = ["ip", "time", "source", "pre", "post", "by", "first_seen"]
REPL_COLS = ["ip", "slot", "old_sn", "new_sn", "date", "reason", "basis", "sn_match", "source", "note"]
REPL_REASONS = ("ce_storm", "ue", "disabled", "post_fail", "other")
REPL_BASIS = ("atlas_page", "bmc_log", "bios", "swap_test", "vendor")

_DATE = re.compile(r"(\d\d)/(\d\d)/(\d{4})")


def sel_kind(event):
    """Memory SEL event class from the ipmitool event text, or None. Order matters: 'Uncorrectable ECC' contains
    'correctable ECC'; the CE logging-limit text contains 'Correctable ECC'."""
    if re.search(r"Uncorrectable ECC|Uncorrectable (machine check|memory)|\bUCE\b", event, re.I):
        return "ue"
    if "logging limit reached" in event.lower():
        return "limit"
    if re.search(r"Correctable ECC|\bCE\b", event, re.I):
        return "ce"
    if re.search(r"Memory Device Disabled|Device Disabled", event, re.I):
        return "disabled"
    return None


def parse_sel_row(sel_id, date_txt, time_txt, sensor, event, direction, day):
    """One ipmitool `sel elist` record -> dict or (None, reason) for memory-like records that cannot be used.

    Returns ({'sel_id','date','time','kind','bmc_date','time_valid','direction'}, None) or (None, reason).
    reason: 'not_memory' (ignore), 'deasserted', 'unknown_event:<text>'. A record with an invalid BMC time (Pre-Init,
    year < 2015, after the collection day + 1) is returned with time_valid='0', date='' and bmc_date=<raw date>; the
    caller fills date with the first-seen day (plan v1.2 MEM-O4)."""
    sensor, event, direction = sensor.strip(), event.strip(), (direction or "").strip()
    memory = sensor.startswith("Memory") or bool(re.search(r"ECC|Memory Device Disabled", event, re.I))
    if not memory:
        return None, "not_memory"
    kind = sel_kind(event)
    if kind is None:
        return None, "unknown_event:" + event[:60]
    if direction == "Deasserted":
        return None, "deasserted"
    m = _DATE.fullmatch(date_txt.strip())
    iso, valid = "", "0"
    if m:
        try:
            d = dt.date(int(m.group(3)), int(m.group(1)), int(m.group(2)))
            if d.year >= 2015 and d <= dt.date.fromisoformat(day) + dt.timedelta(days=1):
                iso, valid = d.isoformat(), "1"
        except ValueError:
            pass
    return {"sel_id": sel_id.strip(), "date": iso, "time": time_txt.strip(), "kind": kind,
            "bmc_date": "" if valid == "1" else (date_txt.strip() or "?"), "time_valid": valid, "direction": direction}, None
DISK_COLS = ["ip", "serial", "slot", "model", "cap_gb", "counter", "before", "after", "day"]
KERN_COLS = ["ip", "day", "class", "count", "first_seen"]
KERN_CLASSES = ("mce", "edac", "mem_offline", "disk_io", "nvme", "ata", "ghes")
DISK_COUNTERS = (("realloc", "重映射扇区"), ("pending", "待映射扇区"), ("media", "介质错误"), ("unc", "上报不可纠正"),
                 ("offunc", "离线不可纠正"), ("errlog", "NVMe 错误日志"), ("crc", "链路 CRC"), ("timeout", "命令超时"))
csv.field_size_limit(1 << 24)


def root():
    return os.environ.get("ATLAS_HW_LEDGER_ROOT", "/data/atlas-research/node-hw-ledger")


def _read(name, cols):
    p = os.path.join(root(), name)
    if not os.path.exists(p):
        return []
    with open(p, newline="", errors="replace") as f:
        r = csv.reader(f, delimiter="\t", quoting=csv.QUOTE_NONE)
        head = next(r, None)
        return [dict(zip(cols, x + [""] * (len(cols) - len(x)))) for x in r] if head else []


def _write(name, cols, rows):
    os.makedirs(root(), exist_ok=True)
    p = os.path.join(root(), name)
    tmp = p + ".tmp"
    with open(tmp, "w", newline="") as f:
        w = csv.writer(f, delimiter="\t", quoting=csv.QUOTE_NONE, escapechar="\\", lineterminator="\n")
        w.writerow(cols)
        for r in rows:
            w.writerow([str(r.get(c, "")).replace("\t", " ").replace("\n", " ") for c in cols])
    os.replace(tmp, p)


def mem_key(r):
    # invalid BMC time: the raw date text (e.g. 'Pre-Init') + raw time keep the key stable across days
    return "|".join((r["ip"], r["sel_id"].lower().lstrip("0"), r.get("bmc_date") or r["date"], r["time"], r["kind"]))


def update_mem(day, records):
    """records: dicts with MEM_COLS (first_seen ignored). Returns (all rows for use, new count)."""
    rows = _read("mem_events.tsv", MEM_COLS)
    for r in rows:
        r["time_valid"] = r.get("time_valid") or "1"
        r["source"] = r.get("source") or "sel"
    idx = {mem_key(r): r for r in rows}
    new = 0
    for rec in records:
        k = mem_key(rec)
        cur = idx.get(k)
        if cur is None and rec.get("time_valid") == "0":
            # hw-ledger-v1 stored future-dated records with their parsed date: re-key that row instead of duplicating
            m = _DATE.fullmatch(rec.get("bmc_date", ""))
            if m:
                legacy = idx.pop("|".join((rec["ip"], rec["sel_id"].lower().lstrip("0"), "%s-%s-%s" % (m.group(3), m.group(1), m.group(2)),
                                           rec["time"], rec["kind"])), None)
                if legacy is not None:
                    legacy.update({"bmc_date": rec["bmc_date"], "time_valid": "0", "date": legacy["first_seen"]})
                    idx[k] = cur = legacy
        if cur is None:
            rec = dict(rec, first_seen=day)
            rec["time_valid"] = rec.get("time_valid") or "1"
            rec["source"] = rec.get("source") or "sel"
            if rec["time_valid"] == "0":
                rec["date"] = day  # first-seen day stands in for the invalid BMC time (MEM-O4)
            rows.append(rec)
            idx[k] = rec
            new += 1
            continue
        if day < cur["first_seen"]:
            cur["first_seen"] = day
            if cur["time_valid"] == "0":
                cur["date"] = day
        if not cur["sn"] and rec.get("sn"):
            for c in ("slot", "bank", "sn", "part", "size", "vendor", "via"):
                cur[c] = rec.get(c, "")
        for c in ("raw", "sensor", "text_loc"):
            if not cur[c] and rec.get(c):
                cur[c] = rec[c]
    rows.sort(key=lambda r: (r["ip"], r["date"], r["time"], r["sel_id"]))
    _write("mem_events.tsv", MEM_COLS, rows)
    return rows, new


def update_disk(day, disks):
    """disks: fleet-report disk dicts carrying 'delta' vs previous collected day."""
    rows = _read("disk_events.tsv", DISK_COLS)
    seen = {(r["serial"], r["counter"], r["day"]) for r in rows}
    new = 0
    for d in disks:
        if not d.get("serial"):
            continue
        for counter, _ in DISK_COUNTERS:
            delta = d.get("delta", {}).get(counter, 0)
            if not delta or delta <= 0 or (d["serial"], counter, day) in seen:
                continue
            after = d.get(counter)
            rows.append({"ip": d["ip"], "serial": d["serial"], "slot": d.get("slot", ""), "model": d.get("model", ""),
                         "cap_gb": d.get("cap_gb") or "", "counter": counter,
                         "before": (after - delta) if isinstance(after, (int, float)) else "", "after": after, "day": day})
            seen.add((d["serial"], counter, day))
            new += 1
    rows.sort(key=lambda r: (r["day"], r["ip"], r["serial"], r["counter"]))
    _write("disk_events.tsv", DISK_COLS, rows)
    return rows, new


def update_kern(day, kern_rows, year):
    """kern_rows: (ip, 'Nov 23', class, count) from kern.tsv; only error classes are kept."""
    import datetime as dt
    rows = _read("kern_events.tsv", KERN_COLS)
    idx = {(r["ip"], r["day"], r["class"]): r for r in rows}
    new = 0
    for ip, mday, cls, cnt in kern_rows:
        if cls not in KERN_CLASSES:
            continue
        try:
            d = dt.datetime.strptime("%s %s" % (mday.strip(), year), "%b %d %Y").date()
            if d.isoformat() > day:  # Dec log lines read in January
                d = d.replace(year=year - 1)
        except ValueError:
            continue
        k = (ip, d.isoformat(), cls)
        try:
            c = int(cnt)
        except ValueError:
            continue
        cur = idx.get(k)
        if cur is None:
            idx[k] = {"ip": ip, "day": d.isoformat(), "class": cls, "count": c, "first_seen": day}
            rows.append(idx[k])
            new += 1
        else:
            cur["count"] = max(int(cur["count"] or 0), c)
            if day < cur["first_seen"]:
                cur["first_seen"] = day
    rows.sort(key=lambda r: (r["day"], r["ip"], r["class"]))
    _write("kern_events.tsv", KERN_COLS, rows)
    return rows, new


def _ts(s):
    try:
        return dt.datetime.fromisoformat(s.replace(" ", "T")[:19])
    except ValueError:
        return None


def update_clears(day, events, merge_hours=6):
    """SEL clear events (MEM-O6). events: dicts with CLEAR_COLS minus first_seen; 'time' = 'YYYY-MM-DD HH:MM:SS'.
    One row per (ip, clear): a new event within merge_hours of an existing one for the same ip is the same clear
    (the VM-derived time is kept when both sources saw it; by=atlas wins). Returns (rows, new)."""
    rows = _read("sel_clear_events.tsv", CLEAR_COLS)
    new = 0
    for ev in events:
        t = _ts(ev["time"])
        same = None
        for r in rows:
            if r["ip"] != ev["ip"]:
                continue
            rt = _ts(r["time"])
            if t and rt and abs((t - rt).total_seconds()) <= merge_hours * 3600:
                same = r
                break
        if same is None:
            rows.append(dict(ev, first_seen=day))
            new += 1
            continue
        if ev["source"] not in same["source"].split("+"):
            same["source"] = "+".join(sorted(set(same["source"].split("+")) | {ev["source"]}))
            if ev["source"] == "vm":
                same["time"] = ev["time"]
            for c in ("pre", "post"):
                if not same[c] and ev.get(c):
                    same[c] = ev[c]
        if ev.get("by") == "atlas":
            same["by"] = "atlas"
        if day < same["first_seen"]:
            same["first_seen"] = day
    rows.sort(key=lambda r: (r["time"], r["ip"]))
    _write("sel_clear_events.tsv", CLEAR_COLS, rows)
    return rows, new


def _valid_sn(sn):
    s = (sn or "").strip().upper()
    return bool(s) and s not in ("NOT SPECIFIED", "UNKNOWN", "N/A", "NA", "NONE", "NO DIMM", "0", "00000000", "SERNUM0",
                                 "NOT AVAILABLE", "[EMPTY]") and not s.startswith("SERNUM")


def update_replacements(day, auto_rows):
    """DIMM replacement ledger (plan v1.2 section 2.2). auto_rows: dicts with REPL_COLS from dmidecode SN changes
    (source=dmidecode) or iBMC MemoryReplaced (source=ibmc). Ops rows come from dimm_replacements.ops.tsv (written by
    people from deploy/node-hw/dimm-replacement-template.tsv; never rewritten here). Key = (ip, old_sn); an ops row
    and an automatic row with the same key become one row: ops supplies reason/basis/sn_match, automatic rows supply
    new_sn/slot/date when ops left them empty. Returns (rows, new_auto)."""
    prev = {(r["ip"], r["old_sn"]): r for r in _read("dimm_replacements.tsv", REPL_COLS) if r.get("source") != "ops"}
    new = 0
    for a in auto_rows:
        k = (a["ip"], a["old_sn"])
        if k not in prev:
            prev[k] = dict(a)
            new += 1
        else:
            cur = prev[k]
            if a["source"] not in cur["source"].split("+"):
                cur["source"] = "+".join(sorted(set(cur["source"].split("+")) | {a["source"]}))
            for c in ("new_sn", "slot", "date"):
                if not cur.get(c) and a.get(c):
                    cur[c] = a[c]
            if a.get("date") and a["date"] < cur.get("date", "9"):
                cur["date"] = a["date"]
    merged = dict(prev)
    p = os.path.join(root(), "dimm_replacements.ops.tsv")
    if os.path.exists(p):
        with open(p, newline="", errors="replace") as f:
            for x in csv.reader(f, delimiter="\t", quoting=csv.QUOTE_NONE):
                if not x or x[0].startswith("#") or x[0] == "ip":
                    continue
                o = dict(zip(REPL_COLS, [v.strip() for v in x] + [""] * (len(REPL_COLS) - len(x))))
                if not o["ip"] or not o["old_sn"]:
                    continue
                o["source"] = "ops"
                k = (o["ip"], o["old_sn"])
                a = merged.get(k)
                if a and a.get("source") != "ops":
                    o["source"] = "ops+" + a["source"]
                    for c in ("new_sn", "slot", "date"):
                        if not o[c]:
                            o[c] = a.get(c, "")
                merged[k] = o
    rows = sorted(merged.values(), key=lambda r: (r.get("date", ""), r["ip"], r.get("slot", "")))
    _write("dimm_replacements.tsv", REPL_COLS, rows)
    return rows, new
