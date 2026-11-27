#!/usr/bin/env python3
"""Read-only Redfish probe for memory CE visibility per platform (MEM-V / D-M1, approved 2026-11-27). Stdlib only.

Only HTTP GET is issued (plus the session-less Basic auth header). No POST/PATCH/DELETE, no event subscription,
no log clearing, no BMC configuration change. Credentials are read from the ipmi_exporter config and are never
printed or written to the output.

Usage (on a host that can reach the BMCs and holds the ipmi_exporter config, e.g. 10.80.32.234):
  python3 node_hw_redfish_probe.py --config /usr/local/exporters/ipmi-exporter/ipmi_remote.yml \
      --targets targets.tsv --out /tmp/redfish-probe-$(date +%F).json
targets.tsv: bmc_ip<TAB>module<TAB>host_ip<TAB>board   (module = ipmi_exporter module name, e.g. nxxz_new)

Daily mode (memory plan v1.2 section 3.5, runs on 10.80.32.234 at 01:00 via scripts/node_hw_redfish_daily.sh):
  python3 node_hw_redfish_probe.py --daily --config <ipmi_remote.yml> --out redfish-mem-<day>.json \
      [--targets t.tsv | --targets-from-vm] [--fleet-url http://10.80.192.232:7077/api/v1/hardware/fleet]
  Minimal requests per platform: XFUSION 2 (Memory $expand + iBMC Log1 first page), Lenovo 1 (EventLog), MSB3
  Memory list + one GET per DIMM, X13DEG MemoryMetrics only on Mondays (or --x13). Each BMC is tried ONCE with the
  module from its target row (no fallback to other credentials: 10.80.168.14 has a 'user locked' history), no
  retry after 401/timeout, at most 8 BMCs in parallel, default exclusions --exclude 10.80.168.14 (host IP).
  Output schema node-hw-redfish-mem-v1: targets[] {host_ip, bmc, module, platform, status, calls, dimms[], events[]}
  events[]: {created (+08:00), kind ue|disabled|replaced|config|other, slot, sn, old_sn, new_sn, message_id, message}.

Output JSON per target: service root vendor/version; Memory collection (per DIMM: Id, DeviceLocator, SerialNumber,
Status, Oem keys, MemoryMetrics CE-related fields); every LogService under Systems/* and Managers/* with entry count
and up to --max-entries newest entries whose Message/MessageId/SensorType mention memory/ECC/DIMM.
"""
from collections import Counter
import argparse, base64, concurrent.futures as cf, datetime as dt, json, os, re, ssl, sys, time, urllib.parse, urllib.request, urllib.error

MEM_RE = re.compile(r"memory|dimm|ecc|correctable|mem\b|ras|mca|ppr", re.I)
CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE


def load_modules(path):
    """Minimal parser for the ipmi_exporter config: modules: <name>: user: / pass:. Uses PyYAML when present."""
    text = open(path).read()
    try:
        import yaml  # type: ignore
        mods = (yaml.safe_load(text) or {}).get("modules", {})
        return {k: (v.get("user", ""), v.get("pass", "")) for k, v in mods.items()}
    except ImportError:
        pass
    mods, cur, in_mod = {}, None, False
    for line in text.splitlines():
        if re.match(r"^modules:\s*$", line):
            in_mod = True
            continue
        if in_mod and re.match(r"^\S", line):
            in_mod = False
        if not in_mod:
            continue
        m = re.match(r"^  (\S[^:]*):\s*$", line)
        if m:
            cur = m.group(1)
            mods[cur] = ["", ""]
            continue
        m = re.match(r"^\s{4,}(user|pass):\s*(.*?)\s*$", line)
        if m and cur:
            v = m.group(2).strip().strip("'\"")
            mods[cur][0 if m.group(1) == "user" else 1] = v
    return {k: tuple(v) for k, v in mods.items()}


class RF:
    def __init__(self, ip, user, pw, timeout):
        self.base = "https://%s" % ip
        self.auth = "Basic " + base64.b64encode(("%s:%s" % (user, pw)).encode()).decode()
        self.timeout = timeout
        self.calls = 0

    def get(self, path):
        self.calls += 1
        req = urllib.request.Request(self.base + path, method="GET",
                                     headers={"Authorization": self.auth, "Accept": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=self.timeout, context=CTX) as r:
                body = r.read()
                return r.status, json.loads(body.decode("utf-8", "replace"))
        except urllib.error.HTTPError as e:
            return e.code, None
        except Exception as e:  # timeout, non-JSON (web UI page), TLS
            return 0, {"_error": type(e).__name__}


def members(rf, path):
    st, j = rf.get(path)
    if st != 200 or not isinstance(j, dict):
        return st, []
    return st, [m.get("@odata.id") for m in j.get("Members", []) if m.get("@odata.id")]


def pick_metrics(j):
    """CE-related fields from a MemoryMetrics resource (DSP0268 2019+: CurrentPeriod/LifeTime CorrectableECCErrorCount,
    HealthData.AlarmTrips.CorrectableECCError) plus any Oem key mentioning ECC/error."""
    out = {}
    for sec in ("CurrentPeriod", "LifeTime"):
        if isinstance(j.get(sec), dict):
            out[sec] = {k: v for k, v in j[sec].items() if "ECC" in k or "Error" in k}
    hd = j.get("HealthData") or {}
    if isinstance(hd.get("AlarmTrips"), dict):
        out["AlarmTrips"] = hd["AlarmTrips"]
    oem = json.dumps(j.get("Oem") or {})
    if re.search(r"ecc|error|correct", oem, re.I):
        out["Oem"] = j.get("Oem")
    return out


def probe(ip, user, pw, args):
    rf = RF(ip, user, pw, args.timeout)
    res = {"bmc": ip, "t0": time.strftime("%F %T")}
    st, root = rf.get("/redfish/v1/")
    res["root_status"] = st
    if st != 200 or not isinstance(root, dict):
        res["verdict"] = "no_redfish"
        return res
    res["vendor"], res["redfish"] = root.get("Vendor") or root.get("Product"), root.get("RedfishVersion")
    st, systems = members(rf, "/redfish/v1/Systems")
    res["systems_status"] = st
    if st == 401:
        res["verdict"] = "auth_failed"
        return res
    dimms, logs = [], []
    for sp in systems[:2]:
        _, sysj = rf.get(sp)
        sysj = sysj or {}
        mem_path = (sysj.get("Memory") or {}).get("@odata.id")
        if mem_path:
            _, mlist = members(rf, mem_path)
            for mp in mlist[: args.max_dimms]:
                _, mj = rf.get(mp)
                if not isinstance(mj, dict) or "_error" in mj:
                    continue
                if (mj.get("Status") or {}).get("State") == "Absent":
                    continue
                d = {"id": mj.get("Id"), "loc": mj.get("DeviceLocator") or (mj.get("MemoryLocation") or {}),
                     "sn": mj.get("SerialNumber"), "status": mj.get("Status"),
                     "oem_keys": sorted((mj.get("Oem") or {}).keys())}
                mm = (mj.get("Metrics") or {}).get("@odata.id")
                if mm:
                    _, mmj = rf.get(mm)
                    if isinstance(mmj, dict):
                        d["metrics"] = pick_metrics(mmj)
                dimms.append(d)
        lsp = (sysj.get("LogServices") or {}).get("@odata.id")
        if lsp:
            logs.append(lsp)
    _, mgrs = members(rf, "/redfish/v1/Managers")
    for mp in mgrs[:2]:
        _, mj = rf.get(mp)
        lsp = ((mj or {}).get("LogServices") or {}).get("@odata.id")
        if lsp:
            logs.append(lsp)
    services = []
    for lsp in logs:
        _, svc = members(rf, lsp)
        for sp in svc:
            st, sj = rf.get(sp)
            ent = ((sj or {}).get("Entries") or {}).get("@odata.id")
            row = {"path": sp, "status": st, "name": (sj or {}).get("Name"), "entries_path": ent}
            if ent:
                st2, ej = rf.get(ent + ("?$top=%d" % args.scan_entries))
                if st2 != 200 or not isinstance(ej, dict) or "_error" in ej:  # some BMCs reject $top
                    st2, ej = rf.get(ent)
                ej = ej if isinstance(ej, dict) else {}
                items = ej.get("Members", [])
                row["count"] = ej.get("Members@odata.count", len(items))
                hits = []
                for e in items:
                    s = " ".join(str(e.get(k, "")) for k in ("Message", "MessageId", "SensorType", "EntryCode", "Name"))
                    if MEM_RE.search(s):
                        hits.append({k: e.get(k) for k in ("Created", "Severity", "Message", "MessageId", "SensorType",
                                                            "EntryCode", "SensorNumber") if e.get(k) is not None})
                row["memory_entries"] = len(hits)
                row["memory_sample"] = hits[: args.max_entries]
            services.append(row)
    res.update({"dimms": len(dimms), "dimm_detail": dimms, "log_services": services, "calls": rf.calls})
    has_metric = any(d.get("metrics") for d in dimms)
    has_log = any(s.get("memory_entries") for s in services)
    res["verdict"] = ("ce_counter" if has_metric else "") + ("+" if has_metric and has_log else "") + \
                     ("memory_log" if has_log else "") or "inventory_only"
    return res


# ------------------------------------------------------------------ daily mode
TZ8 = dt.timezone(dt.timedelta(hours=8))


def platform_of_board(board):
    b = (board or "").upper()
    if "BC16MBSBA" in b or "2258H" in b:
        return "xfusion"
    if "WR5225" in b:
        return "lenovo"
    if "MSB3" in b:
        return "msb3"
    if "X13DEG" in b:
        return "x13"
    return ""


def local_time(created):
    """Redfish 'Created' -> 'YYYY-MM-DDTHH:MM:SS+08:00' (ISO with offset, or 'YYYY/MM/DD HH:MM:SS' already local)."""
    c = (created or "").strip()
    try:
        t = dt.datetime.fromisoformat(c.replace("Z", "+00:00"))
        if t.tzinfo is None:
            t = t.replace(tzinfo=TZ8)
        return t.astimezone(TZ8).isoformat(timespec="seconds")
    except ValueError:
        m = re.match(r"(\d{4})/(\d\d)/(\d\d) (\d\d:\d\d:\d\d)", c)
        return "%s-%s-%sT%s+08:00" % m.groups() if m else c


def classify_event(platform, e):
    msg = str(e.get("Message") or "")
    mid = str(e.get("MessageId") or "")
    slot = re.search(r"DIMM ?_?[A-Z0-9_]+", msg)
    slot = slot.group(0).replace(" ", "") if slot else ""
    sn = re.search(r"\(SN:([0-9A-Za-z]+)\)", msg)
    out = {"created": local_time(e.get("Created")), "slot": slot, "sn": sn.group(1) if sn else "", "message_id": mid,
           "message": msg[:300], "severity": e.get("Severity")}
    if "MemoryReplaced" in mid or re.search(r"is replaced from SN", msg):
        r = re.search(r"from SN\(([0-9A-Za-z]+)\) to SN\(([0-9A-Za-z]+)\)", msg)
        out.update({"kind": "replaced", "old_sn": r.group(1) if r else "", "new_sn": r.group(2) if r else ""})
    elif re.search(r"Uncorrectable|uncorrectable error", mid + " " + msg, re.I) and re.search(r"DIMM|Memory", mid + msg, re.I):
        out["kind"] = "ue"
    elif re.search(r"Memory Device Disabled", msg, re.I):
        out["kind"] = "disabled"
        r = re.search(r"Disabled - (.+)$", msg)
        out["reason"] = r.group(1).strip() if r else ""
    elif re.search(r"Dimm Population|Configuration error", msg, re.I):
        out["kind"] = "config"
    elif platform != "lenovo" and re.search(r"correctable", msg, re.I):
        out["kind"] = "ce"
    else:
        return None
    return out


def poll_one(t, mods, args):
    """One BMC, one credential try, minimal GETs. Never raises."""
    res = {"host_ip": t["host_ip"], "bmc": t["bmc"], "module": t["module"], "platform": t["platform"], "board": t.get("board", ""),
           "status": "ok", "calls": 0, "dimms": [], "events": []}
    if t["module"] not in mods:
        res["status"] = "no_module"
        return res
    u, p = mods[t["module"]]
    rf = RF(t["bmc"], u, p, args.timeout)

    def get(path):
        st, j = rf.get(path)
        res["calls"] = rf.calls
        if st == 401 or st == 403:
            raise PermissionError
        if st == 0:
            raise TimeoutError((j or {}).get("_error", "error"))
        return st, (j if isinstance(j, dict) else {})

    try:
        pf = t["platform"]
        if pf == "xfusion":
            st, j = get("/redfish/v1/Systems/1/Memory?$expand=." + urllib.parse.quote("($levels=1)"))
            mem = j.get("Members", [])
            if mem and "Status" not in mem[0]:
                res["note"] = "expand_unsupported"
                mem = [get(m["@odata.id"])[1] for m in mem[:32] if m.get("@odata.id")]
            for d in mem:
                if (d.get("Status") or {}).get("State") == "Absent":
                    continue
                res["dimms"].append({"loc": d.get("DeviceLocator") or d.get("Id"), "sn": d.get("SerialNumber"),
                                     "health": (d.get("Status") or {}).get("Health"), "state": (d.get("Status") or {}).get("State")})
            st, j = get("/redfish/v1/Systems/1/LogServices/Log1/Entries")
            items = j.get("Members", [])
        elif pf == "lenovo":
            st, j = get("/redfish/v1/Managers/1/LogServices/EventLog/Entries")
            items = j.get("Members", [])
        elif pf in ("msb3", "x13"):
            sysp = "/redfish/v1/Systems/Self" if pf == "msb3" else "/redfish/v1/Systems/1"
            st, j = get(sysp + "/Memory")
            items = []
            for m in j.get("Members", [])[: args.max_dimms]:
                mp = m.get("@odata.id", "")
                if not mp or "/GPU_" in mp:
                    continue
                _, d = get(mp)
                if (d.get("Status") or {}).get("State") == "Absent":
                    continue
                row = {"loc": d.get("DeviceLocator") or d.get("Id"), "sn": d.get("SerialNumber"),
                       "health": (d.get("Status") or {}).get("Health"), "state": (d.get("Status") or {}).get("State")}
                if pf == "x13" and (d.get("Metrics") or {}).get("@odata.id"):
                    _, mm = get(d["Metrics"]["@odata.id"])
                    row["metrics"] = pick_metrics(mm)
                res["dimms"].append(row)
        else:
            res["status"] = "skipped"
            return res
        for e in items:
            ev = classify_event(pf, e)
            if ev:
                res["events"].append(ev)
    except PermissionError:
        res["status"] = "auth_failed"
    except TimeoutError as e:
        res["status"] = "timeout"
        res["error"] = str(e)
    except Exception as e:  # noqa: BLE001
        res["status"] = "error"
        res["error"] = type(e).__name__
    return res


def targets_from_vm(vm, fleet_url):
    """BMC targets: ipmi_up{collector="ipmi"} gives instance (BMC), host_ip, module; board from the Atlas fleet API."""
    q = vm.rstrip("/") + "/query?" + urllib.parse.urlencode({"query": 'max by (instance, host_ip, module) (ipmi_up{collector="ipmi"})'})
    res = json.load(urllib.request.urlopen(q, timeout=60))["data"]["result"]
    boards = {}
    if fleet_url:
        fj = json.load(urllib.request.urlopen(fleet_url, timeout=120))
        for m in (fj.get("data") or fj).get("memory", []):
            boards[m["ip"]] = m.get("board", "")
    out = []
    for r in res:
        m = r["metric"]
        hip = m.get("host_ip", "")
        out.append({"bmc": re.sub(r":\d+$", "", m.get("instance", "")), "module": m.get("module", ""), "host_ip": hip,
                    "board": boards.get(hip, "")})
    return out


def daily(a):
    mods = load_modules(a.config)
    if a.targets:
        tg = []
        for line in open(a.targets):
            if not line.strip() or line.startswith("#"):
                continue
            f = line.rstrip("\n").split("\t") + ["", "", ""]
            tg.append({"bmc": f[0], "module": f[1], "host_ip": f[2], "board": f[3]})
    else:
        tg = targets_from_vm(a.vm, a.fleet_url)
    monday = dt.datetime.now(TZ8).weekday() == 0
    excl = set(a.exclude.split(",")) if a.exclude else set()
    todo, skipped = [], Counter()
    for t in tg:
        t["platform"] = platform_of_board(t["board"])
        if not t["platform"]:
            skipped["no_redfish_need"] += 1
        elif t["host_ip"] in excl or t["bmc"] in excl:
            skipped["excluded"] += 1
        elif t["platform"] == "x13" and not (monday or a.x13):
            skipped["x13_weekly"] += 1
        else:
            todo.append(t)
    with cf.ThreadPoolExecutor(max_workers=max(1, min(8, a.parallel))) as ex:
        results = list(ex.map(lambda t: poll_one(t, mods, a), todo))
    out = {"schema": "node-hw-redfish-mem-v1", "day": dt.datetime.now(TZ8).date().isoformat(),
           "generated_at": dt.datetime.now(TZ8).isoformat(timespec="seconds"), "read_only": True,
           "skipped": dict(skipped), "excluded": sorted(excl), "targets": results,
           "by_status": dict(Counter(r["status"] for r in results))}
    tmp = a.out + ".tmp"
    with open(tmp, "w") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    os.replace(tmp, a.out)
    print("redfish daily: %d polled %s, skipped %s, calls %d" % (len(results), out["by_status"], dict(skipped),
          sum(r["calls"] for r in results)), file=sys.stderr)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", required=True)
    ap.add_argument("--targets")
    ap.add_argument("--out", required=True)
    ap.add_argument("--daily", action="store_true")
    ap.add_argument("--targets-from-vm", action="store_true")
    ap.add_argument("--vm", default=os.environ.get("ATLAS_HW_VM_BASE", "http://10.80.192.252:28481/select/0/prometheus/api/v1"))
    ap.add_argument("--fleet-url", default=os.environ.get("ATLAS_FLEET_URL", "http://10.80.192.232:7077/api/v1/hardware/fleet"))
    ap.add_argument("--exclude", default="10.80.168.14")
    ap.add_argument("--parallel", type=int, default=8)
    ap.add_argument("--x13", action="store_true")
    ap.add_argument("--timeout", type=float, default=20)
    ap.add_argument("--max-dimms", type=int, default=64)
    ap.add_argument("--scan-entries", type=int, default=500)
    ap.add_argument("--max-entries", type=int, default=20)
    a = ap.parse_args()
    if a.daily:
        return daily(a)
    if not a.targets:
        ap.error("--targets is required outside --daily")
    mods = load_modules(a.config)
    out = []
    for line in open(a.targets):
        if not line.strip() or line.startswith("#"):
            continue
        f = line.rstrip("\n").split("\t")
        ip, mod = f[0], (f[1] if len(f) > 1 else "")
        tried = [mod] + [m for m in mods if m != mod] if mod in mods else list(mods)
        r = None
        for m in tried:
            u, p = mods[m]
            r = probe(ip, u, p, a)
            r["module"] = m
            if r["verdict"] != "auth_failed":
                break
        r["host_ip"] = f[2] if len(f) > 2 else ""
        r["board"] = f[3] if len(f) > 3 else ""
        out.append(r)
        print("%-15s %-22s %-14s dimms=%s logs=%s %s" % (ip, r["board"][:22], r["verdict"], r.get("dimms"),
              sum(s.get("memory_entries", 0) for s in r.get("log_services", [])), r.get("vendor")), file=sys.stderr)
        time.sleep(1)  # one BMC at a time, gentle
    json.dump(out, open(a.out, "w"), ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
