package diskhealth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

func ReadLimited(path string, max int64) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("input must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("input must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("input exceeds byte budget")
	}
	return data, nil
}

func Build(manifestPath string, asOf time.Time) (Report, error) {
	result := Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, Mode: "offline_replay", AsOf: asOf.UTC(), Rows: []Result{}}
	if asOf.IsZero() {
		return result, fmt.Errorf("a fixed as-of time is required")
	}
	data, err := ReadLimited(manifestPath, MaxInputBytes)
	if err != nil {
		return result, err
	}
	result.InputManifestSHA256 = Digest(data)
	if err = ValidateJSON(data); err != nil {
		return result, err
	}
	var manifest InputManifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&manifest); err != nil {
		return result, err
	}
	if manifest.Schema != "hardware-health-input-v1" || len(manifest.Inputs) == 0 || len(manifest.Inputs) > MaxInputs {
		return result, fmt.Errorf("unsupported manifest or input count")
	}
	base, err := filepath.EvalSymlinks(filepath.Dir(manifestPath))
	if err != nil {
		return result, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return result, err
	}
	budget := len(data)
	read := func(in Input) ([]byte, error) {
		if filepath.IsAbs(in.File) {
			return nil, fmt.Errorf("input paths must be relative to manifest")
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(base, in.File))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("input escapes manifest directory")
		}
		raw, err := ReadLimited(resolved, MaxInputBytes)
		if err != nil {
			return nil, err
		}
		budget += len(raw)
		if budget > MaxBatchBytes {
			return nil, fmt.Errorf("batch exceeds byte budget")
		}
		if len(in.SHA256) != 64 || Digest(raw) != in.SHA256 {
			return nil, fmt.Errorf("input SHA256 mismatch")
		}
		return raw, nil
	}
	refs := map[string]bool{}
	for _, in := range manifest.Inputs {
		if strings.TrimSpace(in.Reference) == "" || len(in.Reference) > 200 || refs[in.Reference] {
			return result, fmt.Errorf("invalid or duplicate reference")
		}
		refs[in.Reference] = true
		raw, err := read(in)
		if err != nil {
			return result, fmt.Errorf("%s: %w", in.Reference, err)
		}
		var previous []byte
		if in.Baseline != nil {
			if in.Baseline.Baseline != nil {
				return result, fmt.Errorf("nested baselines not supported")
			}
			previous, err = read(*in.Baseline)
			if err != nil {
				return result, fmt.Errorf("%s baseline: %w", in.Reference, err)
			}
		}
		result.Rows = append(result.Rows, Assess(in, raw, previous, asOf))
	}
	identities := map[string][]int{}
	for i, row := range result.Rows {
		if row.IdentityStatus == "bound" {
			identities[row.EntityID] = append(identities[row.EntityID], i)
		}
	}
	for _, indexes := range identities {
		if len(indexes) < 2 {
			continue
		}
		for _, i := range indexes {
			result.Rows[i].IdentityStatus = "conflict"
			result.Rows[i].block("duplicate_or_conflicting_identity")
			result.Rows[i].finish()
		}
	}
	sort.Slice(result.Rows, func(i, j int) bool { return result.Rows[i].Reference < result.Rows[j].Reference })
	result.Summary = Summary{Total: len(result.Rows), ByLevel: map[string]int{}}
	for _, row := range result.Rows {
		result.Summary.ByLevel[row.Level]++
		if row.AssessmentStatus == "assessed" {
			result.Summary.Assessed++
		} else {
			result.Summary.Unknown++
		}
	}
	return result, nil
}

type OutputManifest struct {
	Schema              string `json:"schema"`
	RuleVersion         string `json:"rule_version"`
	InputManifestSHA256 string `json:"input_manifest_sha256"`
	ExecutableSHA256    string `json:"executable_sha256"`
	VCSRevision         string `json:"vcs_revision"`
	VCSModified         bool   `json:"vcs_modified"`
	ReportSHA256        string `json:"report_sha256"`
	MarkdownSHA256      string `json:"markdown_sha256"`
}

func WriteReport(report Report, manifestPath, outDir string) error {
	// A new directory is the publication boundary: never overwrite another run.
	if err := os.Mkdir(outDir, 0700); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(outDir)
		}
	}()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	markdown := []byte(Markdown(report))
	input, err := ReadLimited(manifestPath, MaxInputBytes)
	if err != nil {
		return err
	}
	if Digest(input) != report.InputManifestSHA256 {
		return fmt.Errorf("input manifest changed after assessment")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(executable)
	if err != nil {
		return err
	}
	// Hash the binary without loading its entire contents into memory.
	execHash, err := digestReader(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	meta := OutputManifest{Schema: "hardware-health-artifacts-v1", RuleVersion: report.RuleVersion,
		InputManifestSHA256: Digest(input), ExecutableSHA256: execHash,
		ReportSHA256: Digest(encoded), MarkdownSHA256: Digest(markdown)}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				meta.VCSRevision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				meta.VCSModified = setting.Value == "true"
			}
		}
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	// Carry the original report bytes, not reserialized JSON, for browser digest verification.
	bundle, err := json.Marshal(struct {
		Schema     string `json:"schema"`
		ReportJSON string `json:"report_json"`
		SHA256     string `json:"report_sha256"`
	}{"hardware-health-bundle-v1", string(encoded), meta.ReportSHA256})
	if err != nil {
		return err
	}
	for name, value := range map[string][]byte{"report.json": encoded, "report.md": markdown, "manifest.json": append(metaJSON, '\n'), "bundle.json": append(bundle, '\n')} {
		if err := os.WriteFile(filepath.Join(outDir, name), value, 0600); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

func Markdown(report Report) string {
	escape := func(value string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "\\|", "\n", " ", "\r", " ", "`", "\\`").Replace(value)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Hardware health evidence report\n\nOffline replay at %s. Rule version: %s. No alerts or actions.\n\n", report.AsOf.Format(time.RFC3339), report.RuleVersion)
	fmt.Fprintf(&out, "Records: %d; assessed: %d; insufficient: %d. Counts refer to imported records, not fleet coverage.\n\n", report.Summary.Total, report.Summary.Assessed, report.Summary.Unknown)
	out.WriteString("| Reference | Model / scope | Observed level | Assessment | Blockers |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range report.Rows {
		fmt.Fprintf(&out, "| %s | %s / %s | %s | %s | %s |\n", escape(row.Reference), escape(row.Model), escape(row.EntityType), row.Level, row.AssessmentStatus, escape(strings.Join(row.Blockers, ", ")))
	}
	for _, row := range report.Rows {
		fmt.Fprintf(&out, "\n## %s\n\nInput SHA256: %s\n\n", escape(row.Reference), row.InputSHA256)
		for _, hit := range row.Hits {
			fmt.Fprintf(&out, "- %s: %s; evidence %s\n", hit.Rule, hit.Level, hit.EvidenceID)
		}
		for _, evidence := range row.Evidence {
			fmt.Fprintf(&out, "- %s = %s %s; evidence %s\n", escape(evidence.Field), escape(evidence.Value), escape(evidence.Unit), evidence.ID)
		}
	}
	return out.String()
}
