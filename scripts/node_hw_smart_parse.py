#!/usr/bin/env python3
"""Parse ##SMART sections (smartctl -x -j per device) of node_hw_daily_collect output into one TSV row per disk.

Usage: node_hw_smart_parse.py <collect-day-dir>   (reads nodes/<ip>.txt, writes TSV to stdout)
Columns:
 ip dev dtype protocol model serial firmware capacity_gb power_on_hours temp_c smart_passed
 wear_pct  (NVMe percentage_used / ATA devstat 'Percentage Used Endurance Indicator' / SCSI endurance)
 spare_pct crit_warning media_errors err_log_entries unsafe_shutdowns written_tb
 realloc(5) reported_uncorrect(187) cmd_timeout(188) pending(197) offline_uncorrect(198) crc(199)
 scsi_grown_defects scsi_uncorrected_rw smartctl_exit
Empty = not applicable / not reported. smart_passed=read_error when no usable JSON.
"""
import json, os, re, sys, glob

COLS = ("ip dev dtype protocol model serial firmware capacity_gb power_on_hours temp_c smart_passed wear_pct spare_pct "
        "crit_warning media_errors err_log_entries unsafe_shutdowns written_tb realloc reported_uncorrect cmd_timeout "
        "pending offline_uncorrect crc scsi_grown_defects scsi_uncorrected_rw smartctl_exit").split()


def g(d, *path):
    for p in path:
        if isinstance(d, dict) and p in d:
            d = d[p]
        else:
            return ""
    return d


def row(ip, dev, ty, j):
    r = dict.fromkeys(COLS, "")
    r.update(ip=ip, dev=dev, dtype=ty)
    if not isinstance(j, dict) or not j.get("device"):
        r["smart_passed"] = "read_error"
        r["smartctl_exit"] = g(j, "smartctl", "exit_status") if isinstance(j, dict) else ""
        return r
    r["protocol"] = g(j, "device", "protocol")
    r["model"] = (g(j, "model_name") or g(j, "scsi_model_name") or g(j, "product") or "").strip()
    r["serial"] = (g(j, "serial_number") or "").strip()
    r["firmware"] = (g(j, "firmware_version") or g(j, "scsi_revision") or "").strip()
    cap = g(j, "user_capacity", "bytes") or g(j, "nvme_total_capacity")
    r["capacity_gb"] = round(cap / 1e9) if isinstance(cap, (int, float)) and cap else ""
    r["power_on_hours"] = g(j, "power_on_time", "hours")
    r["temp_c"] = g(j, "temperature", "current")
    sp = g(j, "smart_status", "passed")
    r["smart_passed"] = "" if sp == "" else str(sp).lower()
    r["smartctl_exit"] = g(j, "smartctl", "exit_status")
    nv = j.get("nvme_smart_health_information_log")
    if isinstance(nv, dict):
        r["wear_pct"] = nv.get("percentage_used", "")
        r["spare_pct"] = nv.get("available_spare", "")
        r["crit_warning"] = nv.get("critical_warning", "")
        r["media_errors"] = nv.get("media_errors", "")
        r["err_log_entries"] = nv.get("num_err_log_entries", "")
        r["unsafe_shutdowns"] = nv.get("unsafe_shutdowns", "")
        du = nv.get("data_units_written")
        r["written_tb"] = round(du * 512000 / 1e12, 2) if isinstance(du, (int, float)) else ""
    attrs = {a.get("id"): a for a in g(j, "ata_smart_attributes", "table") or [] if isinstance(a, dict)}
    for k, aid in (("realloc", 5), ("reported_uncorrect", 187), ("cmd_timeout", 188), ("pending", 197),
                   ("offline_uncorrect", 198), ("crc", 199)):
        if aid in attrs:
            r[k] = g(attrs[aid], "raw", "value")
    for page in g(j, "ata_device_statistics", "pages") or []:
        for e in page.get("table", []) if isinstance(page, dict) else []:
            if isinstance(e, dict) and "Percentage Used Endurance" in str(e.get("name", "")):
                r["wear_pct"] = e.get("value", "")
    if r["written_tb"] == "":
        for e in g(j, "ata_device_statistics", "pages") or []:
            for t in e.get("table", []) if isinstance(e, dict) else []:
                if isinstance(t, dict) and t.get("name") == "Logical Sectors Written" and isinstance(t.get("value"), int):
                    r["written_tb"] = round(t["value"] * 512 / 1e12, 2)
    if j.get("scsi_grown_defect_list") is not None:
        r["scsi_grown_defects"] = j.get("scsi_grown_defect_list")
    ec = j.get("scsi_error_counter_log")
    if isinstance(ec, dict):
        r["scsi_uncorrected_rw"] = sum(int(g(ec, op, "total_uncorrected_errors") or 0) for op in ("read", "write"))
    if j.get("scsi_percentage_used_endurance_indicator") is not None:
        r["wear_pct"] = j.get("scsi_percentage_used_endurance_indicator")
    return r


def main(day):
    print("\t".join(COLS))
    for f in sorted(glob.glob(os.path.join(day, "nodes", "*.txt"))):
        ip = os.path.basename(f)[:-4]
        on, dev, ty = False, None, None
        with open(f, errors="replace") as fh:
            for line in fh:
                line = line.rstrip("\n")
                if line.startswith("##"):
                    on = line == "##SMART"
                    continue
                if not on:
                    continue
                if line.startswith("#dev "):
                    parts = line.split()
                    dev, ty = parts[1], (parts[2] if len(parts) > 2 else "")
                    continue
                if dev is None:
                    continue
                try:
                    j = json.loads(line) if line.strip() else None
                except Exception:
                    j = None
                r = row(ip, dev, ty, j)
                # RAID virtual disks (MegaRAID VD exposed as SCSI) are not physical disks; members are read via megaraid,N
                if r["protocol"] == "SCSI" and re.search(r"Logical Volume|AVAGO|^LSI|INSPUR|MR9\d", str(r["model"])):
                    dev = None
                    continue
                print("\t".join(str(r[c]).replace("\t", " ") for c in COLS))
                dev = None


if __name__ == "__main__":
    main(sys.argv[1])
