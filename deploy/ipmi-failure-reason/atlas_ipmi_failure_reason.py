#!/usr/bin/env python3
"""Passive IPMI failure-reason reporter for Atlas.

Follows the ipmi-exporter journal and turns each failing BMC target's latest
error into  atlas_ipmi_failure_reason{ipmi_target,reason} 1  pushed to the local
vminsert. It never talks to a BMC: only targets that already failed to collect
appear in the log, so only those are reported. Entries expire after TTL seconds
without a new error, so a recovered BMC disappears within minutes.
"""
import re, subprocess, sys, threading, time, urllib.request

PUSH_URL = "http://127.0.0.1:28480/insert/0/prometheus/api/v1/import/prometheus"
TTL = 600          # seconds an error stays reported without a refresh
INTERVAL = 30      # push period
BACKFILL = "-10min"
RULES = [  # first match wins; order matters
    ("username invalid", "username_invalid"),
    ("password invalid", "password_invalid"),
    ("connection timeout", "connection_timeout"),
    ("session timeout", "connection_timeout"),
    ("ipmi_sdr_cache", "sdr_cache_error"),
    ("sdr cache", "sdr_cache_error"),
    ("privilege", "privilege_insufficient"),
    ("busy", "bmc_busy"),
]
TARGET = re.compile(r"target=(\d{1,3}(?:\.\d{1,3}){3})\b")
state, lock = {}, threading.Lock()


def classify(line):
    low = line.lower()
    for needle, reason in RULES:
        if needle in low:
            return reason
    return "other"


def follow():
    proc = subprocess.Popen(
        ["journalctl", "-u", "ipmi-exporter", "-f", "-o", "cat", "--since", BACKFILL],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
    for line in proc.stdout:
        if "level=error" not in line or "hostname not specified" in line:
            continue
        m = TARGET.search(line)
        if not m:
            continue
        with lock:
            state[m.group(1)] = (classify(line), time.time())
    code = proc.wait()
    print("journalctl exited with", code, file=sys.stderr, flush=True)
    sys.exit(1)  # systemd restarts us


def push():
    now = time.time()
    with lock:
        for t in [t for t, (_, ts) in state.items() if now - ts > TTL]:
            del state[t]
        rows = sorted((t, r) for t, (r, _) in state.items())
    body = "".join('atlas_ipmi_failure_reason{ipmi_target="%s",reason="%s"} 1\n' % row for row in rows)
    body += 'atlas_ipmi_failure_reason_heartbeat 1\n'
    req = urllib.request.Request(PUSH_URL, data=body.encode(), method="POST")
    urllib.request.urlopen(req, timeout=10).read()
    return len(rows)


def main():
    threading.Thread(target=follow, daemon=True).start()
    time.sleep(5)
    while True:
        try:
            n = push()
            print("pushed", n, "failing targets", flush=True)
        except Exception as e:  # keep running; next cycle retries
            print("push failed:", e, file=sys.stderr, flush=True)
        time.sleep(INTERVAL)
        if not any(t.name == "Thread-1" or t.daemon for t in threading.enumerate()):
            sys.exit(1)


main()
