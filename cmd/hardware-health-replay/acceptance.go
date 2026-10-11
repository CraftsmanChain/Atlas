package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"atlas/internal/diskhealth"
	"atlas/internal/nodeaccess"
)

var generationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var bootIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func buildAcceptanceReport(path, generation, sourceKind string, asOf time.Time) (diskhealth.Report, error) {
	report := diskhealth.Report{}
	if !generationName.MatchString(generation) || (sourceKind != "fixture" && sourceKind != "operator_snapshot") {
		return report, errors.New("audit replay requires an explicit generation and fixture/operator_snapshot source kind")
	}
	raw, err := diskhealth.ReadLimited(path, diskhealth.MaxBatchBytes)
	if err != nil {
		return report, err
	}
	audit, err := nodeaccess.ReadAcceptanceAudit(bytes.NewReader(raw))
	if err != nil {
		return report, err
	}
	fingerprint, fingerprintErr := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(audit.SSHHostFingerprint, "SHA256:"))
	if audit.Schema != nodeaccess.AcceptanceVersion || !audit.NoChangeExecuted || !audit.NoCredentialExposed || audit.HealthAssessment != "not_assessed" ||
		!audit.SSHHostVerified || !strings.HasPrefix(audit.SSHHostFingerprint, "SHA256:") || fingerprintErr != nil || len(fingerprint) != 32 || audit.SSHPort < 1 || audit.SSHPort > 65535 {
		return report, errors.New("audit safety/host metadata missing or invalid (import is not cryptographic attestation)")
	}
	if audit.StartedAt.IsZero() || audit.FinishedAt.Before(audit.StartedAt) || audit.FinishedAt.After(asOf) ||
		audit.FinishedAt.Sub(audit.StartedAt) > time.Duration(audit.Plan.TotalTimeout+audit.Plan.CommandTimeout)*time.Second {
		return report, errors.New("audit time/cutoff invalid")
	}
	if err := nodeaccess.ValidateAcceptancePlan(audit.Plan, audit.StartedAt); err != nil {
		return report, err
	}
	if audit.Status != "completed" && audit.Status != "partial" && audit.Status != "cancelled" {
		return report, errors.New("audit did not reach command collection")
	}
	if len(audit.Evidence) > len(audit.Plan.Commands) || len(audit.Evidence)+len(audit.MissingCommandIDs) != len(audit.Plan.Commands) {
		return report, errors.New("audit coverage mismatch")
	}
	for index, id := range audit.MissingCommandIDs {
		if id != audit.Plan.Commands[len(audit.Evidence)+index].ID {
			return report, errors.New("missing command list mismatch")
		}
	}
	var disk *nodeaccess.AcceptanceEvidence
	bootID, total, completed := "", 0, len(audit.MissingCommandIDs) == 0
	previousFinish := audit.StartedAt
	for index := range audit.Evidence {
		evidence := &audit.Evidence[index]
		if evidence.ID != audit.Plan.Commands[index].ID || !reflect.DeepEqual(evidence.Argv, audit.Plan.Commands[index].Argv) {
			return report, errors.New("evidence argv/plan mismatch")
		}
		if evidence.StartedAt.Before(previousFinish) || evidence.FinishedAt.Before(evidence.StartedAt) || evidence.FinishedAt.After(audit.FinishedAt) ||
			evidence.FinishedAt.Sub(evidence.StartedAt) > time.Duration(audit.Plan.CommandTimeout+audit.Plan.ConnectTimeout)*time.Second {
			return report, errors.New("command time/cutoff invalid")
		}
		previousFinish = evidence.FinishedAt
		if evidence.StdoutSHA256 != diskhealth.Digest(evidence.Stdout) || evidence.StderrSHA256 != diskhealth.Digest(evidence.Stderr) {
			return report, errors.New("command stream digest mismatch")
		}
		bytes := len(evidence.Stdout) + len(evidence.Stderr)
		total += bytes
		if bytes > audit.Plan.CommandOutputBytes || total > audit.Plan.BatchOutputBytes {
			return report, errors.New("command output budget exceeded")
		}
		switch evidence.Status {
		case "completed":
			if !evidence.ExecutionStarted || evidence.ExitCode == nil || *evidence.ExitCode != 0 {
				return report, errors.New("completed command lacks actual zero exit")
			}
		case "command_failed":
			if !evidence.ExecutionStarted || evidence.ExitCode == nil || *evidence.ExitCode <= 0 || *evidence.ExitCode > 255 {
				return report, errors.New("failed command lacks actual nonzero exit")
			}
		case "session_failed", "exit_missing", "exit_signal", "timed_out", "cancelled":
			if evidence.ExitCode != nil {
				return report, errors.New("uncertain execution cannot claim an actual exit")
			}
		default:
			return report, errors.New("unknown command disposition")
		}
		completed = completed && evidence.Status == "completed" && !evidence.RedactionApplied && !evidence.StdoutTruncated && !evidence.StderrTruncated
		if evidence.ID == "disk.smart" {
			disk = evidence
		}
		if evidence.ID == "node.boot_id" && evidence.Status == "completed" && !evidence.RedactionApplied && !evidence.StdoutTruncated && !evidence.StderrTruncated {
			candidate := strings.TrimSpace(string(evidence.Stdout))
			if bootIDPattern.MatchString(candidate) {
				bootID = candidate
			}
		}
	}
	if audit.Status == "completed" && !completed {
		return report, errors.New("completed audit contains incomplete commands")
	}
	if disk == nil || !disk.ExecutionStarted || disk.RedactionApplied {
		return report, errors.New("unmodified executed disk.smart evidence unavailable")
	}
	// Device binding is additional to serial-based identity. Do not let a file
	// collected for another path silently inherit the declared capture scope.
	if !disk.StdoutTruncated {
		var smart, device map[string]json.RawMessage
		var name, protocol string
		if diskhealth.ValidateJSON(disk.Stdout) != nil || json.Unmarshal(disk.Stdout, &smart) != nil ||
			json.Unmarshal(smart["device"], &device) != nil || json.Unmarshal(device["name"], &name) != nil || json.Unmarshal(device["protocol"], &protocol) != nil {
			return report, errors.New("disk stdout is not an unambiguous smartctl object")
		}
		expected := map[string]string{"ata": "ATA", "nvme": "NVMe"}[disk.Argv[2]]
		if name != disk.Argv[len(disk.Argv)-1] || protocol != expected {
			return report, errors.New("SMART device differs from executed argv")
		}
	}
	input := diskhealth.Input{Adapter: "smartctl", Reference: "snapshot-disk", Scope: "node:" + audit.Plan.Node, Node: audit.Plan.Node,
		Generation: generation, BootID: bootID, Source: nodeaccess.AcceptanceVersion, SourceKind: sourceKind,
		ObservedAt: disk.FinishedAt, AvailableAt: audit.FinishedAt, MaxAgeSeconds: 1800, ExitCode: disk.ExitCode,
		Truncated: disk.StdoutTruncated || disk.StderrTruncated}
	row := diskhealth.Assess(input, disk.Stdout, nil, asOf)
	report = diskhealth.Report{Schema: diskhealth.SchemaVersion, InputManifestSHA256: diskhealth.Digest(raw), RuleVersion: diskhealth.ReportRuleVersion,
		Mode: "offline_replay", AsOf: asOf.UTC(), Rows: []diskhealth.Result{row},
		Summary: diskhealth.Summary{Total: 1, ByLevel: map[string]int{row.Level: 1}}}
	if row.AssessmentStatus == "assessed" {
		report.Summary.Assessed = 1
	} else {
		report.Summary.Unknown = 1
	}
	return report, nil
}
