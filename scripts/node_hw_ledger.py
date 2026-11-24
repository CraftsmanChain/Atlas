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
"""
import csv, os

LEDGER_VERSION = "hw-ledger-v1"
MEM_COLS = ["ip", "sel_id", "date", "time", "kind", "text_loc", "sensor", "raw", "slot", "bank", "sn", "part", "size",
            "vendor", "via", "first_seen"]
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
    return "|".join((r["ip"], r["sel_id"].lower().lstrip("0"), r["date"], r["time"], r["kind"]))


def update_mem(day, records):
    """records: dicts with MEM_COLS (first_seen ignored). Returns (all rows for use, new count)."""
    rows = _read("mem_events.tsv", MEM_COLS)
    idx = {mem_key(r): r for r in rows}
    new = 0
    for rec in records:
        k = mem_key(rec)
        cur = idx.get(k)
        if cur is None:
            rec = dict(rec, first_seen=day)
            rows.append(rec)
            idx[k] = rec
            new += 1
            continue
        if day < cur["first_seen"]:
            cur["first_seen"] = day
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
