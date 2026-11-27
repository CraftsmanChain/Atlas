#!/usr/bin/env python3
"""Read-only Redfish probe for memory CE visibility per platform (MEM-V / D-M1, approved 2026-11-27). Stdlib only.

Only HTTP GET is issued (plus the session-less Basic auth header). No POST/PATCH/DELETE, no event subscription,
no log clearing, no BMC configuration change. Credentials are read from the ipmi_exporter config and are never
printed or written to the output.

Usage (on a host that can reach the BMCs and holds the ipmi_exporter config, e.g. 10.80.32.234):
  python3 node_hw_redfish_probe.py --config /usr/local/exporters/ipmi-exporter/ipmi_remote.yml \
      --targets targets.tsv --out /tmp/redfish-probe-$(date +%F).json
targets.tsv: bmc_ip<TAB>module<TAB>host_ip<TAB>board   (module = ipmi_exporter module name, e.g. nxxz_new)

Output JSON per target: service root vendor/version; Memory collection (per DIMM: Id, DeviceLocator, SerialNumber,
Status, Oem keys, MemoryMetrics CE-related fields); every LogService under Systems/* and Managers/* with entry count
and up to --max-entries newest entries whose Message/MessageId/SensorType mention memory/ECC/DIMM.
"""
import argparse, base64, json, re, ssl, sys, time, urllib.request, urllib.error

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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", required=True)
    ap.add_argument("--targets", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--timeout", type=float, default=20)
    ap.add_argument("--max-dimms", type=int, default=64)
    ap.add_argument("--scan-entries", type=int, default=500)
    ap.add_argument("--max-entries", type=int, default=20)
    a = ap.parse_args()
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
