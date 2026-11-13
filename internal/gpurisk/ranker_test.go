package gpurisk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rankerFixture(t *testing.T) Dataset {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := Dataset{Version: DatasetVersion, Target: Target, FeatureContract: HistoryFeatureVersion, Schema: CoreSchema(), PaperCoreComplete: true, ExplorationReady: true, Ready: true, Blockers: []string{}, Choices: []string{"synthetic_fixture_not_production_efficacy"}, Counts: map[string]int{}, Spec: DatasetSpec{BatchIDs: []uint{1, 2, 3}, Anchor: at, TrainEnd: at.AddDate(0, 0, 59), ValidationEnd: at.AddDate(0, 0, 119), TestEnd: at.AddDate(0, 0, 180), Profile: "paper_core"}}
	for i, split := range []string{"train", "validation", "test"} {
		positive, negative := 5, 20
		if i == 0 {
			positive, negative = 10, 60
		}
		for n := 1; n <= positive+negative; n++ {
			yes := n <= positive
			f := HistoryFeatures{Numeric: map[string]*float64{}, Categorical: map[string]*string{}, ByFaultType: map[string]int{"fixture_hardware": 1}, BySeverity: map[string]int{"critical": 1}, EvidenceSHAs: []string{}, Blockers: []string{}}
			for _, field := range CoreSchema() {
				switch field.Dtype {
				case "float64", "nullable_float64":
					v := float64(n % 7)
					f.Numeric[field.Name] = &v
				case "nullable_category":
					v := "fixture-" + field.Name
					f.Categorical[field.Name] = &v
				}
			}
			id := fmt.Sprintf("fixture:%s:%d", split, n)
			f.Categorical["host_id"] = &id
			keys := []string{}
			if yes {
				for j := 0; j < 3; j++ {
					keys = append(keys, fmt.Sprintf("fixture:%s:%d:%d", split, n, j))
				}
			}
			d.Rows = append(d.Rows, DatasetRow{QueryID: uint(i + 1), BatchID: uint(i + 1), NodeID: uint(n), Cutoff: at.AddDate(0, 0, i*60), EntityRef: id, Component: id, Split: split, Features: f, Label: &yes, EpisodeKeys: keys, Eligible: true, Blockers: []string{}})
		}
	}
	d.FeatureSHA, _ = digest(d.Rows)
	d.SplitSHA, _ = digest(d.Spec)
	d.InputSHA, _ = digest([]any{d.Rows, d.Spec})
	d.SHA, _ = digest(d)
	return d
}
func testFile(t *testing.T, path string, value any) string {
	t.Helper()
	data, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
func TestRankerSyntheticDatasetFixture(t *testing.T) {
	if p := os.Getenv("ATLAS_GPU_RISK_MLP_FIXTURE"); p != "" {
		testFile(t, p, rankerFixture(t))
	}
}
func zeroRanker(d Dataset, sha string) RankerArtifact {
	a := RankerArtifact{Version: RankerVersion, Profile: RankerProfile, Target: Target, FeatureContract: HistoryFeatureVersion, DatasetSHA: sha, DatasetManifestSHA: d.SHA, FeatureSHA: d.FeatureSHA, SplitSHA: d.SplitSHA, Purpose: "synthetic_fixture_not_production_efficacy", NoAction: true, Environment: map[string]any{}, Config: map[string]any{"device": "cpu", "dtype": "float32", "loss": "row_mean_BCEWithLogits"}, Selection: map[string]any{"test_used_for_selection": false}}
	for _, f := range CoreSchema() {
		if f.Dtype == "float64" || f.Dtype == "nullable_float64" {
			a.Preprocessor.Numeric = append(a.Preprocessor.Numeric, RankerColumn{Group: "numeric", Name: f.Name, Scale: 1, KnownKeys: []string{}})
		}
		if f.Dtype == "nullable_category" {
			a.Preprocessor.Categories = append(a.Preprocessor.Categories, RankerCategory{Name: f.Name, Vocab: []string{}})
		}
	}
	for _, group := range []string{"by_fault_type", "by_severity"} {
		a.Preprocessor.Numeric = append(a.Preprocessor.Numeric, RankerColumn{Group: group, OOV: true, Scale: 1, KnownKeys: []string{}})
	}
	dimension := 2*len(a.Preprocessor.Numeric) + 2*len(a.Preprocessor.Categories)
	for i, width := range []int{512, 512, 256, 1} {
		l := RankerLayer{Weights: make([][]float64, width), Bias: make([]float64, width)}
		for j := range l.Weights {
			l.Weights[j] = make([]float64, dimension)
		}
		if i < 3 {
			l.BN = &RankerBN{Eps: 1e-5, Mean: make([]float64, width), Variance: make([]float64, width), Gamma: make([]float64, width), Beta: make([]float64, width)}
			for j := range l.BN.Variance {
				l.BN.Variance[j] = 1
				l.BN.Gamma[j] = 1
			}
		}
		a.Layers = append(a.Layers, l)
		dimension = width
	}
	return a
}
func TestRankerPinnedParityRejectsTamperMissingDuplicatesAndNaN(t *testing.T) {
	root := t.TempDir()
	dp, ap, sp := filepath.Join(root, "dataset.json"), filepath.Join(root, "model.json"), filepath.Join(root, "scores.json")
	d := rankerFixture(t)
	ds := testFile(t, dp, d)
	a := zeroRanker(d, ds)
	as := testFile(t, ap, a)
	scores := RankerScores{Version: "gpu-host-mlp-scores-v1", ArtifactSHA: as, DatasetSHA: ds}
	for _, r := range d.Rows {
		scores.Rows = append(scores.Rows, RankerScore{QueryID: r.QueryID, NodeID: r.NodeID, Generation: r.Generation, UncalibratedScore: 0.5})
	}
	ss := testFile(t, sp, scores)
	report, e := VerifyRankerFiles(dp, ds, ap, as, sp, ss)
	if e != nil || report.Rows != 120 || report.MaxAbsError != 0 {
		t.Fatal(report, e)
	}
	original := scores.Rows[0]
	scores.Rows[0].RawScore = 1
	bad := testFile(t, sp, scores)
	if _, e = VerifyRankerFiles(dp, ds, ap, as, sp, bad); e == nil {
		t.Fatal("changed logit accepted")
	}
	scores.Rows[0] = scores.Rows[1]
	bad = testFile(t, sp, scores)
	if _, e = VerifyRankerFiles(dp, ds, ap, as, sp, bad); e == nil {
		t.Fatal("duplicate accepted")
	}
	scores.Rows[0] = original
	scores.Rows = scores.Rows[1:]
	bad = testFile(t, sp, scores)
	if _, e = VerifyRankerFiles(dp, ds, ap, as, sp, bad); e == nil {
		t.Fatal("missing node accepted")
	}
	if _, e = VerifyRankerFiles(dp, ds, ap, as, sp, ss); e == nil {
		t.Fatal("bytes tamper accepted")
	}
	a.Layers[0].Weights[0][0] = math.NaN()
	if validateRanker(a) == nil {
		t.Fatal("NaN weights accepted")
	}
	a.Layers[0].Weights[0][0] = 0
	a.Layers[0].BN.Variance[0] = -1
	if validateRanker(a) == nil {
		t.Fatal("bad BN accepted")
	}
}

func TestRankerScoreRequiresExplicitFiniteFields(t *testing.T) {
	for _, data := range []string{`{"query_id":1,"node_id":1,"generation":0,"uncalibrated_score":0.5}`, `{"query_id":1,"node_id":1,"generation":0,"raw_score":null,"uncalibrated_score":0.5}`, `{"query_id":1,"node_id":1,"generation":0,"raw_score":0,"uncalibrated_score":0.5,"probability":0.5}`} {
		var score RankerScore
		if json.Unmarshal([]byte(data), &score) == nil {
			t.Fatal("missing/null/unknown score field accepted")
		}
	}
}

func TestRankerNullNumbersDoNotBecomeZero(t *testing.T) {
	p := filepath.Join(t.TempDir(), "invalid.json")
	sha := testFile(t, p, map[string]any{"weights": []any{[]any{nil}}})
	var artifact RankerArtifact
	if pinnedJSON(p, sha, &artifact) == nil {
		t.Fatal("numeric null accepted")
	}
}

// Re-pin every dependent file after mutation: rejection must come from the
// semantic guard, not a stale file/manifest SHA or a score mismatch (zero model).
func TestRankerRejectsRepinnedNonCoreDataset(t *testing.T) {
	cases := map[string]func(*Dataset){
		"valid_core": func(d *Dataset) {},
		"schema_extra": func(d *Dataset) {
			d.Schema = append(d.Schema, featureField("telemetry", "float64", "count", "null", "extended"))
		},
		"schema_missing":   func(d *Dataset) { d.Schema = d.Schema[:len(d.Schema)-1] },
		"schema_reordered": func(d *Dataset) { d.Schema[0], d.Schema[1] = d.Schema[1], d.Schema[0] },
		"schema_renamed":   func(d *Dataset) { d.Schema[0].Name = "telemetry" },
		"extended_profile": func(d *Dataset) { d.Spec.Profile = "atlas_extended" },
		"extra_numeric":    func(d *Dataset) { d.Rows[0].Features.Numeric["telemetry"] = numberPtr(1) },
		"extra_numeric_null_excluded": func(d *Dataset) {
			d.Rows[0].Features.Numeric["telemetry"] = nil
			d.Rows[0].Eligible = false
		},
		"extra_category":      func(d *Dataset) { v := "x"; d.Rows[0].Features.Categorical["repair"] = &v },
		"misplaced_core_name": func(d *Dataset) { d.Rows[0].Features.Numeric["host_id"] = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := rankerFixture(t)
			mutate(&d)
			if name != "extended_profile" {
				rows, err := coreFeatureRows(d)
				if name == "valid_core" {
					if err != nil || len(rows) != len(d.Rows) {
						t.Fatalf("valid core fingerprint input rejected: %v", err)
					}
				} else if !errors.Is(err, ErrInput) || rows != nil {
					t.Fatalf("non-core columns reached fingerprint input: %v", err)
				}
			}
			d.FeatureSHA, _ = digest(d.Rows)
			d.SplitSHA, _ = digest(d.Spec)
			d.InputSHA, _ = digest([]any{d.Rows, d.Spec})
			d.SHA = ""
			d.SHA, _ = digest(d)
			root := t.TempDir()
			dp, ap, sp := filepath.Join(root, "dataset.json"), filepath.Join(root, "model.json"), filepath.Join(root, "scores.json")
			ds := testFile(t, dp, d)
			a := zeroRanker(d, ds)
			as := testFile(t, ap, a)
			scores := RankerScores{Version: "gpu-host-mlp-scores-v1", ArtifactSHA: as, DatasetSHA: ds}
			for _, r := range d.Rows {
				scores.Rows = append(scores.Rows, RankerScore{QueryID: r.QueryID, NodeID: r.NodeID, Generation: r.Generation, UncalibratedScore: .5})
			}
			ss := testFile(t, sp, scores)
			_, err := VerifyRankerFiles(dp, ds, ap, as, sp, ss)
			if name == "valid_core" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrInput) {
				t.Fatalf("non-core dataset accepted or wrong error: %v", err)
			}
		})
	}
}
