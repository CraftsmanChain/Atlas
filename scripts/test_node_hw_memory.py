#!/usr/bin/env python3
"""Fixture tests for the memory line v1.2 (rules node-hw-rules-v2.0, ledger hw-ledger-v1.1).

Run: python3 -m unittest scripts/test_node_hw_memory.py   (stdlib only; VictoriaMetrics is switched off)
"""
import gzip, json, os, shutil, subprocess, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import node_hw_ledger as L  # noqa: E402
import node_hw_mem_rules as R  # noqa: E402

GOO, H3C, XF = "10.0.0.1", "10.0.0.2", "10.0.0.3"


def goo_dimms(swap_sn=None):
    out = []
    for cpu in (0, 1):
        for ch in "ABCDEFGH":
            for i in (0, 1):
                sn = "G%d%s%d" % (cpu, ch, i)
                if swap_sn and sn == swap_sn[0]:
                    sn = swap_sn[1]
                out.append([GOO, "DIMM %d" % i, "32 GB", "Samsung", sn, "M321R4GA3BB6", "4800 MT/s", "P%d CHANNEL %s" % (cpu, ch), "4800 MT/s", "2"])
    return out


def write_day(root, day, sel, selraw, dimms, nodes_extra="", selinfo=None, redfish=None, kern=None):
    d = os.path.join(root, day)
    os.makedirs(os.path.join(d, "nodes"), exist_ok=True)
    nodes = [(GOO, "gpu_node", "goo"), (H3C, "cpu_node", "h3c"), (XF, "cpu_node", "xf")]
    with open(os.path.join(d, "nodes.tsv"), "w") as f:
        f.write("".join("%s\t%s\t%s\n" % n for n in nodes))
    with open(os.path.join(d, "status.tsv"), "w") as f:
        f.write("ip\tkind\tssh_rc\tstatus\tflags\n")
        f.write("%s\tgpu_node\t0\tok\tsel_full\n%s\tcpu_node\t0\tok\t\n%s\tcpu_node\t0\tok\t\n" % (GOO, H3C, XF))
    for name in ("excluded.tsv", "followup.tsv", "smart.tsv", "storcli.tsv"):
        open(os.path.join(d, name), "w").close()
    with open(os.path.join(d, "sel.tsv"), "w") as f:
        f.write("".join("\t".join(r) + "\n" for r in sel))
    with open(os.path.join(d, "selraw.tsv"), "w") as f:
        f.write("".join("\t".join(r) + "\n" for r in selraw))
    with open(os.path.join(d, "dimm.tsv"), "w") as f:
        f.write("".join("\t".join(r) + "\n" for r in dimms))
    with open(os.path.join(d, "board.tsv"), "w") as f:
        f.write("%s\tGOOXI\tG3DCTR-B PCBA\n%s\tH3C\tRS33M2C9S\n%s\tXFUSION\tBC16MBSBA\n" % (GOO, H3C, XF))
    with open(os.path.join(d, "kern.tsv"), "w") as f:
        f.write("".join("\t".join(r) + "\n" for r in (kern or [])))
    with open(os.path.join(d, "selinfo.tsv"), "w") as f:
        f.write("ip\tentries\tused_pct\tfree_bytes\tlast_add\tlast_del\toverflow\tbmc_time\tnode_time\tsel_records\ttime_invalid\n")
        for ip, (ent, ld) in (selinfo or {}).items():
            f.write("%s\t%s\t\t\t\t%s\t\t\t\t%s\t0\n" % (ip, ent, ld, ent))
    for ip, hc in ((GOO, 0), (H3C, 0), (XF, 0)):
        txt = "##META\ncollector=node-hw-collect-v2\nproduct=X\n##MEM\nMemTotal:       1056000000 kB\nHardwareCorrupted:  %d kB\n%s##END\n" % (hc, nodes_extra)
        with gzip.open(os.path.join(d, "nodes", ip + ".txt.gz"), "wt") as f:
            f.write(txt)
    if redfish:
        json.dump({"schema": "node-hw-redfish-mem-v1", "targets": redfish}, open(os.path.join(d, "redfish-mem.json"), "w"))
    return d


class MemoryV12(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.root = os.path.join(self.tmp, "daily")
        self.led = os.path.join(self.tmp, "ledger")
        os.makedirs(self.root)
        os.makedirs(self.led)
        self.env = dict(os.environ, ATLAS_HW_VM_OFF="1", ATLAS_HW_LEDGER_ROOT=self.led,
                        ATLAS_HW_EXCLUDE_FILE=os.path.join(self.tmp, "none.tsv"),
                        ATLAS_HW_MEM_PLATFORMS=os.path.join(HERE, "..", "configs", "node-hw-mem-platforms.tsv"))
        self._old_root = os.environ.get("ATLAS_HW_LEDGER_ROOT")
        os.environ["ATLAS_HW_LEDGER_ROOT"] = self.led  # the ledger reads in this process see the same files

    def tearDown(self):
        if self._old_root is None:
            os.environ.pop("ATLAS_HW_LEDGER_ROOT", None)
        else:
            os.environ["ATLAS_HW_LEDGER_ROOT"] = self._old_root
        shutil.rmtree(self.tmp)

    def run_report(self, day_dir):
        p = subprocess.run([sys.executable, os.path.join(HERE, "node_hw_fleet_report.py"), day_dir], env=self.env,
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        with open(os.path.join(day_dir, "fleet-hw.json")) as f:
            return json.load(f)

    def test_parse_sel_row(self):
        r, why = L.parse_sel_row("1", "Pre-Init", "0000000123", "Memory #0xd3", "Correctable ECC", "Asserted", "2026-11-27")
        self.assertEqual((r["kind"], r["time_valid"], r["bmc_date"]), ("ce", "0", "Pre-Init"))
        r, _ = L.parse_sel_row("2", "11/26/2026", "1:00:00", "Memory #0xd3", "Correctable ECC logging limit reached", "Asserted", "2026-11-27")
        self.assertEqual(r["kind"], "limit")
        r, _ = L.parse_sel_row("3", "01/01/2000", "1:00:00", "Memory", "Uncorrectable ECC", "Asserted", "2026-11-27")
        self.assertEqual((r["kind"], r["time_valid"]), ("ue", "0"))
        r, why = L.parse_sel_row("4", "11/26/2026", "1:00:00", "Memory #0xd3", "Correctable ECC", "Deasserted", "2026-11-27")
        self.assertEqual((r, why), (None, "deasserted"))
        _, why = L.parse_sel_row("5", "11/26/2026", "1:00:00", "Temperature #0x1", "Upper critical", "Asserted", "2026-11-27")
        self.assertEqual(why, "not_memory")

    def test_channel_of(self):
        self.assertEqual(R.channel_of({"slot": "DIMM 0", "bank": "P1 CHANNEL C"}), ("1", "C"))
        self.assertEqual(R.channel_of({"slot": "P1-DIMMA2", "bank": ""}), ("1", "A"))
        self.assertEqual(R.channel_of({"slot": "DIMM_J1", "bank": ""}), ("1", "J"))
        self.assertEqual(R.channel_of({"slot": "CPU1_DIMM_B0", "bank": ""}), ("1", "B"))

    def test_platform_matrix_default_limited(self):
        m, ver = R.load_platforms(self.env["ATLAS_HW_MEM_PLATFORMS"])
        self.assertEqual(ver, "mem-platforms-v1")
        self.assertEqual(R.platform_of(m, "H3C RS33M2C9SA", "")["ce_source"], "sel_inband")
        self.assertEqual(R.platform_of(m, "OEM BC16MBSBA", "")["ce_source"], "none")
        self.assertIsNone(R.platform_of(m, "Unknown Board X", ""))

    def test_two_days(self):
        # day 1: GOOXI storm on one DIMM (P0 CHANNEL B DIMM 0) + 30 Pre-Init CE; H3C UE; XFUSION nothing in SEL
        sel1, raw1 = [], []
        for i in range(60):
            sid = "%x" % (i + 1)
            sel1.append([GOO, sid, "11/26/2026", "10:%02d:00" % (i % 60), "Memory #0xd3", "Correctable ECC", "Asserted"])
            raw1.append([GOO, "%04x" % (i + 1), "d3", "a01000"])
        for i in range(30):
            sel1.append([GOO, "%x" % (200 + i), "Pre-Init", "00000%05d" % i, "Memory #0xd3", "Correctable ECC", "Asserted"])
        sel1.append([H3C, "10", "11/26/2026", "03:00:00", "Memory CPU1_DIMM_A0", "Uncorrectable ECC", "Asserted"])
        h3c_dimms = [[H3C, "CPU1_DIMM_A0", "16 GB", "Hynix", "H1", "HMA", "3200 MT/s", "", "2933 MT/s", "2"],
                     [H3C, "CPU1_DIMM_B0", "32 GB", "Hynix", "H2", "HMB", "3200 MT/s", "", "3200 MT/s", "2"]]
        xf_dimms = [[XF, "DIMM000", "64 GB", "Samsung", "S1", "M3", "4800 MT/s", "", "4800 MT/s", "2"],
                    [XF, "DIMM120", "64 GB", "Samsung", "177C84C1", "M3", "4800 MT/s", "", "4800 MT/s", "2"]]
        d1 = write_day(self.root, "2026-11-26", sel1, raw1, goo_dimms() + h3c_dimms + xf_dimms,
                       selinfo={GOO: ("1364", "11/20/2026 10:00:00")})
        rep1 = self.run_report(d1)
        mem = {m["ip"]: m for m in rep1["memory"]}
        g = mem[GOO]
        self.assertEqual(rep1["rule_version"], "node-hw-rules-v2.0")
        self.assertEqual(g["ce_7d"], 60)                       # Pre-Init records are not in windows
        self.assertEqual(g["time_invalid"], 30)
        self.assertIn("mem_o4_bmc_time_invalid", g["observability"]["codes"])
        self.assertIn("mem_o1_sel_full", g["observability"]["codes"])
        storm = [e for e in g["dimm_issues"] if e["sn"] == "G0B0"]
        self.assertEqual(len(storm), 1)                        # R-1: one row per SN
        self.assertEqual(storm[0]["ce_total"], 60)
        self.assertIn("mem_h4_ce_storm", storm[0]["hits"])     # 60 CE within 24 h
        self.assertEqual(mem[H3C]["level"], "critical")         # R-3: H3C UE parsed
        self.assertIn("mem_h1_ue", mem[H3C]["hits"])
        self.assertEqual(mem[H3C]["observability"]["level"], "full")
        self.assertIn("mem_s4_config", mem[H3C]["hits"])        # mixed sizes + below rated speed
        self.assertEqual(mem[XF]["observability"]["level"], "limited")  # MEM-O2: blind platform is never "full"
        self.assertIn("mem_o2_platform_no_ce", mem[XF]["observability"]["codes"])
        n1 = len(L._read("mem_events.tsv", L.MEM_COLS))
        self.assertEqual(n1, 91)
        self.run_report(d1)                                    # idempotent re-run
        self.assertEqual(len(L._read("mem_events.tsv", L.MEM_COLS)), n1)

        # day 2: GOOXI SEL cleared externally (entries 1364 -> 40), DIMM P0 CHANNEL C DIMM 0 swapped by ops (ce_storm),
        # XFUSION Redfish: vendor UE on 177C84C1 + MemoryReplaced, MSB3-style unhealthy DIMM S1
        sel2 = [[GOO, "%x" % (i + 1), "11/27/2026", "00:%02d:00" % i, "Memory #0xd3", "Correctable ECC", "Asserted"] for i in range(5)]
        raw2 = [[GOO, "%04x" % (i + 1), "d3", "a02100"] for i in range(5)]     # P1 CHANNEL C DIMM 0
        rf = [{"host_ip": XF, "bmc": "10.200.0.3", "platform": "xfusion", "status": "ok",
               "dimms": [{"loc": "DIMM000", "sn": "S1", "health": "Warning", "state": "Enabled"},
                         {"loc": "DIMM120", "sn": "177C84C1", "health": "OK", "state": "Enabled"}],
               "events": [{"created": "2026-11-26T11:50:12+00:00", "kind": "ue", "slot": "DIMM120", "sn": "177C84C1",
                           "message_id": "iBMCEvents.19.11.MemoryUncorrectableErr", "message": "DIMM120 triggered an uncorrectable error"},
                          {"created": "2026-11-27T00:10:00+00:00", "kind": "replaced", "slot": "DIMM000", "old_sn": "S0", "new_sn": "S1",
                           "message_id": "iBMCEvents.19.11.MemoryReplaced", "message": "DIMM000 is replaced"}]},
              {"host_ip": H3C, "bmc": "x", "platform": "h3c", "status": "auth_failed", "dimms": [], "events": []}]
        with open(os.path.join(self.led, "dimm_replacements.ops.tsv"), "w") as f:
            f.write("ip\tslot\told_sn\tnew_sn\tdate\treason\tbasis\tsn_match\tsource\tnote\n")
            f.write("%s\tDIMM 0 / P0 CHANNEL C\tG0C0\t\t2026-11-27\tce_storm\tatlas_page\tyes\tops\t\n" % GOO)
        d2 = write_day(self.root, "2026-11-27", sel2, raw2, goo_dimms(("G0C0", "NEWSN")) + h3c_dimms[:1] + xf_dimms,
                       selinfo={GOO: ("40", "11/26/2026 16:20:00")}, redfish=rf)
        rep2 = self.run_report(d2)
        mem = {m["ip"]: m for m in rep2["memory"]}
        g = mem[GOO]
        self.assertIn("mem_o6_sel_cleared", g["observability"]["codes"])
        clears = L._read("sel_clear_events.tsv", L.CLEAR_COLS)
        self.assertEqual([(c["ip"], c["by"], c["source"]) for c in clears], [(GOO, "external", "selinfo")])
        self.assertEqual(g["ce_7d"], 65)                        # ledger keeps day-1 CE after the clear
        repl = L._read("dimm_replacements.tsv", L.REPL_COLS)
        goo_r = [r for r in repl if r["ip"] == GOO]
        self.assertEqual(len(goo_r), 1)
        self.assertEqual((goo_r[0]["source"], goo_r[0]["new_sn"], goo_r[0]["reason"]), ("ops+dmidecode", "NEWSN", "ce_storm"))
        self.assertTrue(any(r["source"] == "ibmc" and r["old_sn"] == "S0" for r in repl))
        x = mem[XF]
        self.assertIn("mem_h1_ue", x["hits"])                   # vendor-log UE (MEM-H1)
        self.assertEqual(x["vendor_ue_30d"], 1)
        self.assertIn("mem_h15_bmc_health", x["hits"])           # Redfish Health != OK
        h = mem[H3C]
        self.assertIn("mem_s1_dimm_missing", h["hits"])           # one DIMM gone vs day 1
        self.assertNotIn("mem_o7_redfish_unreadable", h["observability"]["codes"])  # H3C does not depend on Redfish
        rs = rep2["summary"]["memory"]["replacements"]
        self.assertEqual(rs["atlas_page"], 1)
        self.assertEqual(rs["censored_ce_storm"], 1)
        self.assertTrue(os.path.exists(os.path.join(d2, "mem-report.md")))
        self.assertTrue(os.path.exists(os.path.join(d2, "mem_state.tsv")))
        kinds = {e["kind"] for e in rep2["events"] if e["type"] == "memory"}
        self.assertTrue({"replaced", "sel_clear", "ue"} <= kinds, kinds)


if __name__ == "__main__":
    unittest.main()
