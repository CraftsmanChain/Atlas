package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/diskhealth"
	"atlas/internal/nodeaccess"
)

func publicDiskAudit(t *testing.T) nodeaccess.AcceptanceAudit {
	t.Helper()
	start := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	plan, err := nodeaccess.PlanAcceptance(nodeaccess.AcceptanceScope{Schema: "hardware-evidence-scope-v1", ScopeID: "synthetic-audit", Node: "192.0.2.1", Username: "fixture", CredentialProfileID: "fixture-profile",
		IssuedAt: start.Add(-time.Minute), ExpiresAt: start.Add(10 * time.Minute), Commands: []nodeaccess.AcceptanceRequest{{ID: "node.boot_id"}, {ID: "disk.smart", Device: "/dev/sdb", Protocol: "ata"}}}, start)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../internal/diskhealth/testdata/smart-ata.json")
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	audit := nodeaccess.AcceptanceAudit{Schema: nodeaccess.AcceptanceVersion, Plan: plan, Status: "completed", StartedAt: start, FinishedAt: start.Add(4 * time.Second), MissingCommandIDs: []string{}, NoChangeExecuted: true, NoCredentialExposed: true, HealthAssessment: "not_assessed", SSHHostVerified: true, SSHHostFingerprint: "SHA256:" + strings.Repeat("A", 43), SSHPort: 22}
	for i, stdout := range [][]byte{[]byte("00000000-0000-0000-0000-000000000001\n"), raw} {
		command := plan.Commands[i]
		audit.Evidence = append(audit.Evidence, nodeaccess.AcceptanceEvidence{ID: command.ID, Argv: command.Argv, Status: "completed", ExecutionStarted: true, ExitCode: &exit, StartedAt: start.Add(time.Duration(2*i) * time.Second), FinishedAt: start.Add(time.Duration(2*i+1) * time.Second), Stdout: stdout, StdoutSHA256: diskhealth.Digest(stdout), StderrSHA256: diskhealth.Digest(nil)})
	}
	return audit
}

func saveAudit(t *testing.T, audit nodeaccess.AcceptanceAudit) string {
	t.Helper()
	raw, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAcceptanceBridgeEndToEnd(t *testing.T) {
	audit := publicDiskAudit(t)
	if fixturePath := os.Getenv("ATLAS_SYNTHETIC_AUDIT_OUTPUT"); fixturePath != "" {
		data, err := json.MarshalIndent(audit, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(fixturePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("synthetic fixture output failed")
		}
	}
	path := saveAudit(t, audit)
	outDir := filepath.Join(t.TempDir(), "report")
	var out, stderr bytes.Buffer
	if run([]string{"--acceptance-audit", path, "--generation", "fixture-1", "--source-kind", "fixture", "--as-of", audit.FinishedAt.Format(time.RFC3339), "--out-dir", outDir}, &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report diskhealth.Report
	if json.Unmarshal(raw, &report) != nil || report.Summary.Assessed != 1 || report.Rows[0].Disk.MediaType != "HDD" || report.Rows[0].SourceKind != "fixture" || report.Rows[0].BootID == "" || report.ActionsExecuted || report.AlertsEmitted {
		t.Fatalf("invalid bridge: %s", raw)
	}
	if report.InputManifestSHA256 == report.Rows[0].InputSHA256 {
		t.Fatal("audit and device stream hashes conflated")
	}
}

func TestAcceptanceBridgeSafety(t *testing.T) {
	for name, mutate := range map[string]func(*nodeaccess.AcceptanceAudit){
		"unsafe_argv": func(a *nodeaccess.AcceptanceAudit) {
			a.Plan.Commands[1].Argv[0] = "sh"
			a.Evidence[1].Argv = a.Plan.Commands[1].Argv
		},
		"unsafe_budget": func(a *nodeaccess.AcceptanceAudit) { a.Plan.CommandOutputBytes++ },
		"stream_tamper": func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].Stdout = append(a.Evidence[1].Stdout, ' ') },
		"stderr_tamper": func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].Stderr = []byte("oops") },
		"device_mismatch": func(a *nodeaccess.AcceptanceAudit) {
			a.Plan.Commands[1].Argv[7] = "/dev/sda"
			a.Evidence[1].Argv = a.Plan.Commands[1].Argv
		},
		"not_verified":        func(a *nodeaccess.AcceptanceAudit) { a.SSHHostVerified = false },
		"missing_fingerprint": func(a *nodeaccess.AcceptanceAudit) { a.SSHHostFingerprint = "" },
		"redacted":            func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].RedactionApplied = true; a.Status = "partial" },
		"invalid_time":        func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].FinishedAt = a.FinishedAt.Add(time.Second) },
		"fake_success":        func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].ExitCode = nil },
		"missing_coverage":    func(a *nodeaccess.AcceptanceAudit) { a.MissingCommandIDs = []string{"disk.smart"} },
		"device_not_executed": func(a *nodeaccess.AcceptanceAudit) { a.Evidence[1].ExecutionStarted = false },
		"not_readonly":        func(a *nodeaccess.AcceptanceAudit) { a.NoChangeExecuted = false },
	} {
		t.Run(name, func(t *testing.T) {
			audit := publicDiskAudit(t)
			mutate(&audit)
			if _, err := buildAcceptanceReport(saveAudit(t, audit), "fixture-1", "fixture", audit.FinishedAt); err == nil {
				t.Fatal("unsafe audit accepted")
			}
		})
	}
	audit := publicDiskAudit(t)
	path := saveAudit(t, audit)
	for _, args := range [][]string{{"--acceptance-audit", path}, {"--manifest", path, "--acceptance-audit", path, "--generation", "1"}} {
		var out, err bytes.Buffer
		if run(append(args, "--as-of", audit.FinishedAt.Format(time.RFC3339), "--out-dir", filepath.Join(t.TempDir(), "out")), &out, &err) != 2 {
			t.Fatal("ambiguous CLI accepted")
		}
	}
	if _, err := buildAcceptanceReport(path, "1", "fixture", audit.FinishedAt.Add(-time.Second)); err == nil {
		t.Fatal("future audit accepted")
	}
}

func TestAcceptanceBridgeRetainsActualDisposition(t *testing.T) {
	for _, scenario := range []string{"nonzero", "missing_exit", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			audit := publicDiskAudit(t)
			e := &audit.Evidence[1]
			audit.Status = "partial"
			var smart map[string]any
			if err := json.Unmarshal(e.Stdout, &smart); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "nonzero":
				exit := 8
				e.ExitCode = &exit
				e.Status = "command_failed"
				smart["smartctl"].(map[string]any)["exit_status"] = 8
				smart["smart_status"].(map[string]any)["passed"] = false
			case "missing_exit":
				e.ExitCode = nil
				e.Status = "exit_missing"
			case "truncated":
				e.StdoutTruncated = true
			}
			e.Stdout, _ = json.Marshal(smart)
			e.StdoutSHA256 = diskhealth.Digest(e.Stdout)
			report, err := buildAcceptanceReport(saveAudit(t, audit), "fixture-1", "fixture", audit.FinishedAt)
			if err != nil {
				t.Fatal(err)
			}
			row := report.Rows[0]
			if scenario == "nonzero" {
				if row.Level != "critical" || row.ExitCode == nil || *row.ExitCode != 8 {
					t.Fatal("nonzero SMART evidence lost")
				}
			} else if row.AssessmentStatus != "insufficient_data" || row.Level != "unknown" {
				t.Fatal("uncertain execution became healthy")
			}
		})
	}
}

func TestAuditDecoderRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{`{"status":"completed","Status":"partial"}`, `{} {}`, `{"unknown":true}`} {
		if _, err := nodeaccess.ReadAcceptanceAudit(strings.NewReader(raw)); err == nil {
			t.Fatal("ambiguous audit accepted")
		}
	}
}
