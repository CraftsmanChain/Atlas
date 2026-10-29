package gpurisk

// File-only verification: never registers a model, writes scores or connects to a DB.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"
)

const RankerVersion = "gpu-host-mlp-artifact-v1"
const RankerProfile = "hearank-paper-spec-v1"

type RankerColumn struct {
	Group     string   `json:"group"`
	Name      string   `json:"name"`
	OOV       bool     `json:"oov"`
	KnownKeys []string `json:"known_keys"`
	Mean      float64  `json:"mean"`
	Scale     float64  `json:"scale"`
}
type RankerCategory struct {
	Name  string   `json:"name"`
	Vocab []string `json:"vocab"`
}
type RankerPreprocessor struct {
	Numeric    []RankerColumn   `json:"numeric"`
	Categories []RankerCategory `json:"categories"`
}
type RankerBN struct {
	Mean     []float64 `json:"mean"`
	Variance []float64 `json:"variance"`
	Gamma    []float64 `json:"gamma"`
	Beta     []float64 `json:"beta"`
	Eps      float64   `json:"eps"`
}
type RankerLayer struct {
	Weights [][]float64 `json:"weights"`
	Bias    []float64   `json:"bias"`
	BN      *RankerBN   `json:"bn,omitempty"`
}
type RankerArtifact struct {
	Version            string             `json:"version"`
	Profile            string             `json:"profile"`
	Target             string             `json:"target"`
	FeatureContract    string             `json:"feature_contract"`
	DatasetSHA         string             `json:"dataset_sha"`
	DatasetManifestSHA string             `json:"dataset_manifest_sha"`
	FeatureSHA         string             `json:"feature_sha"`
	SplitSHA           string             `json:"split_sha"`
	Purpose            string             `json:"purpose"`
	Preprocessor       RankerPreprocessor `json:"preprocessor"`
	Layers             []RankerLayer      `json:"layers"`
	Config             map[string]any     `json:"config"`
	Environment        map[string]any     `json:"environment"`
	Selection          map[string]any     `json:"selection"`
	ProbabilityEmitted bool               `json:"probability_emitted"`
	NoAction           bool               `json:"no_action"`
}
type RankerScore struct {
	QueryID           uint    `json:"query_id"`
	NodeID            uint    `json:"node_id"`
	Generation        int     `json:"generation"`
	RawScore          float64 `json:"raw_score"`
	UncalibratedScore float64 `json:"uncalibrated_score"`
}

// Required score fields must not silently decode as zero (including null).
func (s *RankerScore) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return ErrInput
	}
	for _, key := range []string{"query_id", "node_id", "generation", "raw_score", "uncalibrated_score"} {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInput
		}
	}
	type plain RankerScore
	return json.Unmarshal(raw, (*plain)(s))
}

type RankerScores struct {
	Version     string        `json:"version"`
	ArtifactSHA string        `json:"artifact_sha"`
	DatasetSHA  string        `json:"dataset_sha"`
	Rows        []RankerScore `json:"rows"`
}
type RankerVerification struct {
	Version            string  `json:"version"`
	ArtifactSHA        string  `json:"artifact_sha"`
	DatasetSHA         string  `json:"dataset_sha"`
	ScoresSHA          string  `json:"scores_sha"`
	Rows               int     `json:"rows"`
	MaxAbsError        float64 `json:"max_abs_error"`
	NearTiePairs       int     `json:"near_tie_pairs"`
	Purpose            string  `json:"purpose"`
	Status             string  `json:"status"`
	NoAction           bool    `json:"no_action"`
	ProbabilityEmitted bool    `json:"probability_emitted"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func pinnedJSON(path, sha string, out any) error {
	if !validSHA(sha) {
		return ErrInput
	}
	raw, e := readBoundedFile(path)
	if e != nil {
		return ErrInput
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != sha {
		return ErrInput
	}
	// Model/score numeric nulls must never become Go zero values. Dataset nulls
	// remain legitimate explicit unknown features and are handled separately.
	switch out.(type) {
	case *RankerArtifact, *RankerScores:
		var values any
		if json.Unmarshal(raw, &values) != nil || jsonNull(values) {
			return ErrInput
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInput
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInput
	}
	return nil
}
func readBoundedFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrInput
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 64*1024*1024+1))
	if e != nil || len(data) > 64*1024*1024 {
		return nil, ErrInput
	}
	return data, nil
}

func jsonNull(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range v {
			if jsonNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if jsonNull(item) {
				return true
			}
		}
	}
	return false
}

func uniqueStrings(v []string) bool {
	seen := map[string]bool{}
	for _, s := range v {
		if seen[s] || len(s) > 256 {
			return false
		}
		seen[s] = true
	}
	return true
}
func rankerVector(p RankerPreprocessor, f HistoryFeatures) ([]float64, error) {
	x := []float64{}
	for _, c := range p.Numeric {
		var value *float64
		if c.Group == "numeric" {
			v, ok := f.Numeric[c.Name]
			if !ok {
				return nil, ErrInput
			}
			value = v
		} else {
			counts := f.ByFaultType
			if c.Group == "by_severity" {
				counts = f.BySeverity
			}
			if counts != nil {
				v := 0.
				if c.OOV {
					for k, n := range counts {
						if n < 0 {
							return nil, ErrInput
						}
						if !contains(c.KnownKeys, k) {
							v += float64(n)
						}
					}
				} else {
					v = float64(counts[c.Name])
				}
				value = &v
			}
		}
		a, b := 0., 1.
		if value != nil {
			a = (*value - c.Mean) / c.Scale
			b = 0
		}
		if !finite(a) || math.Abs(a) >= 1e30 {
			return nil, ErrInput
		}
		x = append(x, a, b)
	}
	for _, c := range p.Categories {
		v, ok := f.Categorical[c.Name]
		if !ok {
			return nil, ErrInput
		}
		index := 0
		if v != nil {
			index = 1
			for i, k := range c.Vocab {
				if *v == k {
					index = i + 2
					break
				}
			}
		}
		for i := 0; i < len(c.Vocab)+2; i++ {
			v := 0.
			if i == index {
				v = 1
			}
			x = append(x, v)
		}
	}
	return x, nil
}
func validateRanker(a RankerArtifact) error {
	if a.Version != RankerVersion || a.Profile != RankerProfile || a.Target != Target || a.FeatureContract != HistoryFeatureVersion || a.ProbabilityEmitted || !a.NoAction || (a.Purpose != "offline_exploration_not_promoted" && a.Purpose != "synthetic_fixture_not_production_efficacy") || a.Config["device"] != "cpu" || a.Config["dtype"] != "float32" || a.Config["loss"] != "row_mean_BCEWithLogits" || a.Selection["test_used_for_selection"] != false {
		return ErrInput
	}
	numeric, categories := map[string]bool{}, map[string]bool{}
	for _, field := range CoreSchema() {
		if field.Dtype == "nullable_float64" || field.Dtype == "float64" {
			numeric[field.Name] = true
		}
		if field.Dtype == "nullable_category" {
			categories[field.Name] = true
		}
	}
	seen := map[string]bool{}
	oov := map[string]bool{}
	for _, c := range a.Preprocessor.Numeric {
		key := c.Group + ":" + c.Name
		if c.OOV {
			key += "/oov"
		}
		if seen[key] || !finite(c.Mean) || !finite(c.Scale) || c.Scale <= 0 || !uniqueStrings(c.KnownKeys) || len(c.KnownKeys) > 128 {
			return ErrInput
		}
		seen[key] = true
		switch c.Group {
		case "numeric":
			if !numeric[c.Name] || c.OOV || len(c.KnownKeys) != 0 {
				return ErrInput
			}
			delete(numeric, c.Name)
		case "by_fault_type", "by_severity":
			if c.OOV {
				if c.Name != "" || oov[c.Group] {
					return ErrInput
				}
				oov[c.Group] = true
			} else if !contains(c.KnownKeys, c.Name) {
				return ErrInput
			}
		default:
			return ErrInput
		}
	}
	if len(numeric) != 0 || !oov["by_fault_type"] || !oov["by_severity"] {
		return ErrInput
	}
	dimension := 2 * len(a.Preprocessor.Numeric)
	for _, c := range a.Preprocessor.Categories {
		if !categories[c.Name] || !uniqueStrings(c.Vocab) || len(c.Vocab) > 1024 {
			return ErrInput
		}
		delete(categories, c.Name)
		dimension += len(c.Vocab) + 2
	}
	if len(categories) != 0 || dimension > 4096 || len(a.Layers) != 4 {
		return ErrInput
	}
	widths := []int{512, 512, 256, 1}
	for i, l := range a.Layers {
		width := widths[i]
		if len(l.Weights) != width || len(l.Bias) != width {
			return ErrInput
		}
		for n, row := range l.Weights {
			if len(row) != dimension || !finite(l.Bias[n]) {
				return ErrInput
			}
			for _, v := range row {
				if !finite(v) {
					return ErrInput
				}
			}
		}
		if i < 3 {
			b := l.BN
			if b == nil || b.Eps != 1e-5 || len(b.Mean) != width || len(b.Variance) != width || len(b.Gamma) != width || len(b.Beta) != width {
				return ErrInput
			}
			for j := 0; j < width; j++ {
				if !finite(b.Mean[j]) || !finite(b.Variance[j]) || b.Variance[j] < 0 || !finite(b.Gamma[j]) || !finite(b.Beta[j]) {
					return ErrInput
				}
			}
		} else if l.BN != nil {
			return ErrInput
		}
		dimension = width
	}
	return nil
}
func rankerLogit(a RankerArtifact, f HistoryFeatures) (float64, error) {
	x, e := rankerVector(a.Preprocessor, f)
	if e != nil {
		return 0, e
	}
	for _, l := range a.Layers {
		y := make([]float64, len(l.Bias))
		for i, row := range l.Weights {
			v := l.Bias[i]
			for j, w := range row {
				v += w * x[j]
			}
			if b := l.BN; b != nil {
				v = (v-b.Mean[i])/math.Sqrt(b.Variance[i]+b.Eps)*b.Gamma[i] + b.Beta[i]
				v = math.Max(0, v)
			}
			if !finite(v) {
				return 0, ErrInput
			}
			y[i] = v
		}
		x = y
	}
	return x[0], nil
}

// VerifyRankerFiles checks the exact caller-pinned files and independently
// evaluates every node in Go. Passing verifies software parity, never promotion.
func VerifyRankerFiles(datasetPath, datasetSHA, artifactPath, artifactSHA, scoresPath, scoresSHA string) (RankerVerification, error) {
	report := RankerVerification{Version: RankerVersion, ArtifactSHA: artifactSHA, DatasetSHA: datasetSHA, ScoresSHA: scoresSHA, NoAction: true, Status: "offline_parity_verified"}
	var d Dataset
	var a RankerArtifact
	var scores RankerScores
	if pinnedJSON(datasetPath, datasetSHA, &d) != nil || pinnedJSON(artifactPath, artifactSHA, &a) != nil || pinnedJSON(scoresPath, scoresSHA, &scores) != nil {
		return report, ErrInput
	}
	expectedSchema, _ := digest(CoreSchema())
	actualSchema, _ := digest(d.Schema)
	if expectedSchema != actualSchema || !validSHA(d.FeatureSHA) || !validSHA(d.SplitSHA) || !validSHA(d.InputSHA) {
		return report, ErrInput
	}
	original := d.SHA
	d.SHA = ""
	actual, e := digest(d)
	d.SHA = original
	if e != nil || actual != original || d.Version != DatasetVersion || d.FeatureContract != HistoryFeatureVersion || d.Target != Target || d.Spec.Profile != "paper_core" || len(d.Rows) == 0 || len(d.Rows) > 10000 || a.DatasetSHA != datasetSHA || a.DatasetManifestSHA != d.SHA || a.FeatureSHA != d.FeatureSHA || a.SplitSHA != d.SplitSHA || scores.Version != "gpu-host-mlp-scores-v1" || scores.DatasetSHA != datasetSHA || scores.ArtifactSHA != artifactSHA || validateRanker(a) != nil {
		return report, ErrInput
	}
	if len(d.Rows)*len(a.Layers[0].Weights[0]) > 2000000 {
		return report, ErrInput
	}
	if (a.Purpose == "synthetic_fixture_not_production_efficacy") != contains(d.Choices, "synthetic_fixture_not_production_efficacy") {
		return report, ErrInput
	}
	if len(scores.Rows) != len(d.Rows) {
		return report, ErrInput
	}
	type key struct {
		query, node uint
		generation  int
	}
	byKey := map[key]RankerScore{}
	scoreNodes := map[[2]uint]bool{}
	for _, s := range scores.Rows {
		nodeKey := [2]uint{s.QueryID, s.NodeID}
		if scoreNodes[nodeKey] {
			return report, ErrInput
		}
		scoreNodes[nodeKey] = true
		k := key{s.QueryID, s.NodeID, s.Generation}
		if _, ok := byKey[k]; ok || !finite(s.RawScore) || !finite(s.UncalibratedScore) || s.UncalibratedScore < 0 || s.UncalibratedScore > 1 {
			return report, ErrInput
		}
		byKey[k] = s
	}
	seen := map[key]bool{}
	byQuery := map[uint][][2]float64{}
	for _, r := range d.Rows {
		k := key{r.QueryID, r.NodeID, r.Generation}
		s, ok := byKey[k]
		if !ok || seen[k] {
			return report, ErrInput
		}
		seen[k] = true
		z, e := rankerLogit(a, r.Features)
		if e != nil {
			return report, e
		}
		delta := math.Abs(z - s.RawScore)
		report.MaxAbsError = math.Max(report.MaxAbsError, delta)
		if delta > 1e-6+1e-5*math.Abs(s.RawScore) {
			return report, ErrInput
		}
		sigmoid := 1 / (1 + math.Exp(-z))
		if math.Abs(sigmoid-s.UncalibratedScore) > 1e-6+1e-5*math.Abs(sigmoid) {
			return report, ErrInput
		}
		byQuery[r.QueryID] = append(byQuery[r.QueryID], [2]float64{z, s.RawScore})
	}
	for _, rows := range byQuery {
		sort.Slice(rows, func(i, j int) bool { return rows[i][0] > rows[j][0] })
		for i := 1; i < len(rows); i++ {
			gap := rows[i-1][0] - rows[i][0]
			tol := 2e-6 + 1e-5*(math.Abs(rows[i-1][0])+math.Abs(rows[i][0]))
			if gap <= tol {
				report.NearTiePairs++
			} else if rows[i-1][1] < rows[i][1] {
				return report, ErrInput
			}
		}
	}
	report.Rows = len(d.Rows)
	report.Purpose = a.Purpose
	return report, nil
}
