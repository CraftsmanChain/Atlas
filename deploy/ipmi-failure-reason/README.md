# atlas-ipmi-failure-reason

Runs on the **ipmi-exporter host** (production: `10.80.32.234`), not on the Atlas server.

It follows the `ipmi-exporter` systemd journal and publishes why each failing BMC
could not be collected, so Atlas can show the exact cause on Data Quality → Target
Coverage → IPMI Exporter:

```
atlas_ipmi_failure_reason{ipmi_target="10.80.248.4",reason="password_invalid"} 1
```

Reasons: `username_invalid`, `password_invalid`, `connection_timeout`,
`sdr_cache_error`, `privilege_insufficient`, `bmc_busy`, `other`.

## Properties

- Passive: reads the exporter log only and never contacts a BMC, so only nodes that
  already failed collection appear, and no BMC audit log entries are produced.
- An entry expires 10 minutes after its last error, so recovered BMCs disappear.
- Pushes every 30 s to the local vminsert
  (`http://127.0.0.1:28480/insert/0/prometheus/api/v1/import/prometheus`); a
  `atlas_ipmi_failure_reason_heartbeat` series shows the reporter is alive.
- Runs as a transient `DynamicUser` in group `systemd-journal`, 128 MiB memory cap,
  `Restart=always`, enabled at boot. Credentials are never read or stored.

Atlas consumes it in `internal/inventory/ipmi_health.go` (`ipmiCauseCode`).

## Install / update (as root on the exporter host)

```bash
install -m 755 atlas_ipmi_failure_reason.py /usr/local/exporters/ipmi-exporter/atlas_ipmi_failure_reason.py
install -m 644 atlas-ipmi-failure-reason.service /etc/systemd/system/atlas-ipmi-failure-reason.service
systemctl daemon-reload
systemctl enable --now atlas-ipmi-failure-reason.service
journalctl -u atlas-ipmi-failure-reason -n 5 --no-pager   # expect "pushed N failing targets"
```

If the vminsert address or the exporter unit name differ, edit `PUSH_URL` in the
script and `journalctl -u ipmi-exporter` in `follow()`.

## Roll back

```bash
systemctl disable --now atlas-ipmi-failure-reason.service
rm /etc/systemd/system/atlas-ipmi-failure-reason.service /usr/local/exporters/ipmi-exporter/atlas_ipmi_failure_reason.py
systemctl daemon-reload
```

Atlas then falls back to the generic reasons (`ipmi_collector_all_failed` /
`ipmi_collector_failed`).
