package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
)

const (
	baselineModelVersion    = "gpu-logistic-baseline-v4"
	cohortReadinessGateName = "fault-model-horizon-readiness-v1"
)

type BaselineModelBuildRequest struct {
	SourceMatrixBuildID uint   `json:"source_matrix_build_id"`
	EventType           string `json:"event_type,omitempty"`
	ModelName           string `json:"model_name,omitempty"`
}

type logisticModel struct {
	HorizonMinutes int       `json:"horizon_minutes"`
	FeatureColumns []string  `json:"feature_columns"`
	Means          []float64 `json:"means"`
	Scales         []float64 `json:"scales"`
	Coefficients   []float64 `json:"coefficients"`
	Intercept      float64   `json:"intercept"`
	Threshold      float64   `json:"threshold"`
}
type baselineMetrics struct {
	Count     int     `json:"count"`
	Positive  int     `json:"positive"`
	Control   int     `json:"control"`
	ROCAUC    float64 `json:"roc_auc"`
	PRAUC     float64 `json:"pr_auc"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Brier     float64 `json:"brier"`
}
type baselineHorizonReport struct {
	HorizonMinutes          int                        `json:"horizon_minutes"`
	Train                   baselineMetrics            `json:"train"`
	Validation              baselineMetrics            `json:"validation"`
	Test                    baselineMetrics            `json:"test"`
	TestByModel             map[string]baselineMetrics `json:"test_by_model"`
	TestByEventType         map[string]baselineMetrics `json:"test_by_event_type"`
	TestByDriverVersion     map[string]baselineMetrics `json:"test_by_driver_version"`
	TestByLabelSource       map[string]baselineMetrics `json:"test_by_label_source"`
	TestByHardwareCertainty map[string]baselineMetrics `json:"test_by_hardware_certainty"`
	TestByRuleVersion       map[string]baselineMetrics `json:"test_by_rule_version"`
	Threshold               float64                    `json:"threshold"`
}
type baselineArtifact struct {
	Version        string          `json:"version"`
	Algorithm      string          `json:"algorithm"`
	MatrixKey      string          `json:"matrix_key"`
	ScopeEventType string          `json:"scope_event_type,omitempty"`
	ScopeModelName string          `json:"scope_model_name,omitempty"`
	ReadinessGate  string          `json:"readiness_gate,omitempty"`
	FeaturePolicy  string          `json:"feature_policy"`
	Models         []logisticModel `json:"models"`
	CreatedAt      time.Time       `json:"created_at"`
}
type baselineReport struct {
	Version                 string                     `json:"version"`
	Algorithm               string                     `json:"algorithm"`
	MatrixKey               string                     `json:"matrix_key"`
	ScopeEventType          string                     `json:"scope_event_type,omitempty"`
	ScopeModelName          string                     `json:"scope_model_name,omitempty"`
	ReadinessGate           string                     `json:"readiness_gate,omitempty"`
	Mode                    string                     `json:"mode"`
	FeaturePolicy           string                     `json:"feature_policy"`
	CalibrationPolicy       string                     `json:"calibration_policy"`
	Horizons                []baselineHorizonReport    `json:"horizons"`
	MacroTest               baselineMetrics            `json:"macro_test"`
	ByTestModel             map[string]baselineMetrics `json:"by_test_model"`
	ByTestEventType         map[string]baselineMetrics `json:"by_test_event_type"`
	ByTestDriverVersion     map[string]baselineMetrics `json:"by_test_driver_version"`
	ByTestLabelSource       map[string]baselineMetrics `json:"by_test_label_source"`
	ByTestHardwareCertainty map[string]baselineMetrics `json:"by_test_hardware_certainty"`
	ByTestRuleVersion       map[string]baselineMetrics `json:"by_test_rule_version"`
	CreatedAt               time.Time                  `json:"created_at"`
}

func (s *Service) BaselineModelBuilds(limit int) ([]api.BaselineModelBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.BaselineModelBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) BaselineModelReport(id uint) (baselineReport, error) {
	var build api.BaselineModelBuild
	if err := s.db.First(&build, id).Error; err != nil {
		return baselineReport{}, err
	}
	if build.Status != "completed" || build.ReportPath == "" {
		return baselineReport{}, fmt.Errorf("completed baseline report is required")
	}
	base, err := filepath.Abs(s.config.DatasetDir)
	if err != nil {
		return baselineReport{}, err
	}
	path, err := filepath.Abs(build.ReportPath)
	if err != nil {
		return baselineReport{}, err
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return baselineReport{}, fmt.Errorf("baseline report path is outside the configured dataset directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return baselineReport{}, err
	}
	defer file.Close()
	var report baselineReport
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&report); err != nil {
		return baselineReport{}, fmt.Errorf("decode baseline report: %w", err)
	}
	return report, nil
}

func (s *Service) StartBaselineModelBuild(request BaselineModelBuildRequest) (api.BaselineModelBuild, error) {
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	if s.baselineRunning {
		return api.BaselineModelBuild{}, fmt.Errorf("baseline training is already running")
	}
	request.EventType = strings.TrimSpace(request.EventType)
	request.ModelName = strings.TrimSpace(request.ModelName)
	if (request.EventType == "") != (request.ModelName == "") {
		return api.BaselineModelBuild{}, fmt.Errorf("event_type and model_name must be provided together")
	}
	var matrix api.TrainingMatrixBuild
	query := s.db.Where("status = ? AND version = ?", "completed", trainingMatrixVersion)
	if request.SourceMatrixBuildID > 0 {
		query = query.Where("id = ?", request.SourceMatrixBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&matrix)
	if result.Error != nil {
		return api.BaselineModelBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.BaselineModelBuild{}, fmt.Errorf("a completed supervised training matrix is required")
	}
	started := s.now()
	key := baselineModelVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	readinessGate := ""
	if request.EventType != "" {
		readinessGate = cohortReadinessGateName
	}
	build := api.BaselineModelBuild{BaselineModelKey: key, Version: baselineModelVersion, Status: "queued", Algorithm: "logistic_regression",
		SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion,
		ScopeEventType: request.EventType, ScopeModelName: request.ModelName, ReadinessGateVersion: readinessGate,
		OutputDir: filepath.Join(s.config.DatasetDir, "baseline-models", key), StartedAt: started}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	s.baselineRunning = true
	go s.executeBaselineModelBuild(build.ID)
	return build, nil
}
func (s *Service) executeBaselineModelBuild(id uint) {
	defer func() { s.baselineMu.Lock(); s.baselineRunning = false; s.baselineMu.Unlock() }()
	var build api.BaselineModelBuild
	if s.db.First(&build, id).Error != nil {
		return
	}
	if s.db.Model(&build).Update("status", "running").Error != nil {
		return
	}
	if err := s.buildBaselineModels(&build); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished}).Error
	}
}

func (s *Service) buildBaselineModels(build *api.BaselineModelBuild) error {
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&matrix, build.SourceMatrixBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(matrix.MatrixPath, matrix.MatrixSHA256); err != nil {
		return fmt.Errorf("matrix checksum: %w", err)
	}
	rows, err := readJSONLines[trainingMatrixRow](matrix.MatrixPath)
	if err != nil {
		return err
	}
	if build.ScopeEventType != "" {
		rows, err = scopedReadyMatrixRows(matrix.TrainingMatrixKey, rows, build.ScopeEventType, build.ScopeModelName)
		if err != nil {
			return err
		}
	}
	columns := safeBaselineColumns(rows)
	if len(columns) == 0 {
		return fmt.Errorf("no pre-failure-safe feature columns")
	}
	build.FeatureColumnCount = len(columns)
	byHorizon := map[int][]trainingMatrixRow{}
	for _, row := range rows {
		byHorizon[row.HorizonMinutes] = append(byHorizon[row.HorizonMinutes], row)
	}
	horizons := make([]int, 0, len(byHorizon))
	for h := range byHorizon {
		horizons = append(horizons, h)
	}
	sort.Ints(horizons)
	artifact := baselineArtifact{Version: baselineModelVersion, Algorithm: "logistic_regression", MatrixKey: matrix.TrainingMatrixKey, ScopeEventType: build.ScopeEventType, ScopeModelName: build.ScopeModelName, ReadinessGate: build.ReadinessGateVersion, FeaturePolicy: baselineFeaturePolicy(), CreatedAt: s.now()}
	report := baselineReport{Version: baselineModelVersion, Algorithm: "logistic_regression", MatrixKey: matrix.TrainingMatrixKey, ScopeEventType: build.ScopeEventType, ScopeModelName: build.ScopeModelName, ReadinessGate: build.ReadinessGateVersion, Mode: "offline_evaluation_only", FeaturePolicy: baselineFeaturePolicy(), CalibrationPolicy: "uncalibrated baseline; validation selects an F1 threshold and test remains untouched; no online probability release", ByTestModel: map[string]baselineMetrics{}, ByTestEventType: map[string]baselineMetrics{}, ByTestDriverVersion: map[string]baselineMetrics{}, ByTestLabelSource: map[string]baselineMetrics{}, ByTestHardwareCertainty: map[string]baselineMetrics{}, ByTestRuleVersion: map[string]baselineMetrics{}, CreatedAt: s.now()}
	for _, h := range horizons {
		train, val, test := splitMatrixRows(byHorizon[h])
		if !hasBothLabels(train) || !hasBothLabels(val) || !hasBothLabels(test) {
			return fmt.Errorf("horizon %d requires both labels in every split", h)
		}
		model := fitLogistic(train, columns, h)
		validationScores := scoreRows(model, val)
		model.Threshold = bestF1Threshold(validationScores)
		testScores := scoreRows(model, test)
		for i := range testScores {
			testScores[i].threshold = model.Threshold
		}
		artifact.Models = append(artifact.Models, model)
		report.Horizons = append(report.Horizons, baselineHorizonReport{HorizonMinutes: h, Train: describeLabels(train), Validation: evaluateScores(validationScores, model.Threshold), Test: evaluateScores(testScores, model.Threshold), TestByModel: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return []string{row.ModelName} }), TestByEventType: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.EventTypes }), TestByDriverVersion: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.DriverVersions }), TestByLabelSource: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.LabelSources }), TestByHardwareCertainty: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.HardwareCertainties }), TestByRuleVersion: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.RuleDecisionVersions }), Threshold: model.Threshold})
		build.TrainCount += len(train)
		build.ValidationCount += len(val)
		build.TestCount += len(test)
	}
	report.ByTestModel = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByModel })
	report.ByTestEventType = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByEventType })
	report.ByTestDriverVersion = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByDriverVersion })
	report.ByTestLabelSource = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByLabelSource })
	report.ByTestHardwareCertainty = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByHardwareCertainty })
	report.ByTestRuleVersion = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByRuleVersion })
	report.MacroTest = macroTestMetrics(report.Horizons)
	build.HorizonCount = len(horizons)
	build.TrainedModelCount = len(artifact.Models)
	build.TestMacroROCAUC = report.MacroTest.ROCAUC
	build.TestMacroPRAUC = report.MacroTest.PRAUC
	build.TestMacroPrecision = report.MacroTest.Precision
	build.TestMacroRecall = report.MacroTest.Recall
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	artifactPath := filepath.Join(build.OutputDir, "models.json")
	if err := writeJSONAtomic(artifactPath, artifact); err != nil {
		return err
	}
	checksum, err := fileSHA256(artifactPath)
	if err != nil {
		return err
	}
	reportPath := filepath.Join(build.OutputDir, "evaluation_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{"status": "completed", "feature_column_count": build.FeatureColumnCount, "horizon_count": build.HorizonCount, "trained_model_count": build.TrainedModelCount, "train_count": build.TrainCount, "validation_count": build.ValidationCount, "test_count": build.TestCount, "test_macro_roc_auc": build.TestMacroROCAUC, "test_macro_pr_auc": build.TestMacroPRAUC, "test_macro_precision": build.TestMacroPrecision, "test_macro_recall": build.TestMacroRecall, "artifact_path": artifactPath, "artifact_sha256": checksum, "report_path": reportPath, "finished_at": &finished}).Error
}

func stratifiedTestMetrics(rows []trainingMatrixRow, scores []scoredLabel, labels func(trainingMatrixRow) []string) map[string]baselineMetrics {
	grouped := map[string][]scoredLabel{}
	for index, row := range rows {
		appendStratifiedScore(grouped, labels(row), scores[index])
	}
	return evaluateStratifiedScores(grouped)
}

func appendStratifiedScore(grouped map[string][]scoredLabel, labels []string, score scoredLabel) {
	if len(labels) == 0 {
		grouped["unknown"] = append(grouped["unknown"], score)
		return
	}
	seen := map[string]bool{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		grouped[label] = append(grouped[label], score)
	}
	if len(seen) == 0 {
		grouped["unknown"] = append(grouped["unknown"], score)
	}
}

func evaluateStratifiedScores(grouped map[string][]scoredLabel) map[string]baselineMetrics {
	result := make(map[string]baselineMetrics, len(grouped))
	for label, scores := range grouped {
		result[label] = evaluateScores(scores, -1)
	}
	return result
}

func macroStratifiedMetrics(horizons []baselineHorizonReport, selectMetrics func(baselineHorizonReport) map[string]baselineMetrics) map[string]baselineMetrics {
	totals := map[string]baselineMetrics{}
	counts := map[string]int{}
	for _, horizon := range horizons {
		for label, metrics := range selectMetrics(horizon) {
			total := totals[label]
			total.Count += metrics.Count
			total.Positive += metrics.Positive
			total.Control += metrics.Control
			total.ROCAUC += metrics.ROCAUC
			total.PRAUC += metrics.PRAUC
			total.Precision += metrics.Precision
			total.Recall += metrics.Recall
			total.F1 += metrics.F1
			total.Brier += metrics.Brier
			totals[label] = total
			counts[label]++
		}
	}
	for label, total := range totals {
		count := float64(counts[label])
		total.ROCAUC /= count
		total.PRAUC /= count
		total.Precision /= count
		total.Recall /= count
		total.F1 /= count
		total.Brier /= count
		totals[label] = total
	}
	return totals
}

func baselineFeaturePolicy() string {
	return "exclude XID, ECC, row-remap and reset indicators that represent an occurred fault; use only pre-failure thermal, power, utilization, clock, framebuffer and PCIe-replay trends"
}
func safeBaselineColumns(rows []trainingMatrixRow) []string {
	set := map[string]bool{}
	excluded := []string{"xid_", "correctable_remapped_rows_", "uncorrectable_remapped_rows_", "row_remap_failure_", "uncorrected_ecc_", "gpu_reset_required_"}
	for _, r := range rows {
		for c := range r.Features {
			safe := !strings.Contains(c, "_sample_count_")
			for _, p := range excluded {
				if strings.HasPrefix(c, p) {
					safe = false
					break
				}
			}
			if safe {
				set[c] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func scopedReadyMatrixRows(matrixKey string, rows []trainingMatrixRow, eventType, modelName string) ([]trainingMatrixRow, error) {
	readiness := evaluateCohortReadiness(matrixKey, rows)
	readyHorizons := map[int]bool{}
	for _, stratum := range readiness.Strata {
		if stratum.EventType == eventType && stratum.ModelName == modelName && stratum.Status == "exploratory_ready" {
			readyHorizons[stratum.HorizonMinutes] = true
		}
	}
	if len(readyHorizons) == 0 {
		return nil, fmt.Errorf("no exploratory-ready horizon for event_type=%q model_name=%q", eventType, modelName)
	}
	result := make([]trainingMatrixRow, 0, len(rows))
	for _, row := range rows {
		if row.ModelName == modelName && readyHorizons[row.HorizonMinutes] && containsString(row.LabelMetadata.EventTypes, eventType) {
			result = append(result, row)
		}
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func splitMatrixRows(rows []trainingMatrixRow) (train, val, test []trainingMatrixRow) {
	for _, r := range rows {
		switch r.Split {
		case "train":
			train = append(train, r)
		case "validation":
			val = append(val, r)
		case "test":
			test = append(test, r)
		}
	}
	return
}
func hasBothLabels(rows []trainingMatrixRow) bool {
	a, b := false, false
	for _, r := range rows {
		if r.LabelValue == 1 {
			a = true
		} else {
			b = true
		}
	}
	return a && b
}
func describeLabels(rows []trainingMatrixRow) baselineMetrics {
	m := baselineMetrics{Count: len(rows)}
	for _, r := range rows {
		if r.LabelValue == 1 {
			m.Positive++
		} else {
			m.Control++
		}
	}
	return m
}

func fitLogistic(rows []trainingMatrixRow, cols []string, h int) logisticModel {
	n := len(cols)
	mean := make([]float64, n)
	scale := make([]float64, n)
	observed := make([]int, n)
	for _, r := range rows {
		for j, c := range cols {
			if value, exists := r.Features[c]; exists {
				mean[j] += value
				observed[j]++
			}
		}
	}
	for j := range mean {
		if observed[j] > 0 {
			mean[j] /= float64(observed[j])
		}
	}
	for _, r := range rows {
		for j, c := range cols {
			if value, exists := r.Features[c]; exists {
				d := value - mean[j]
				scale[j] += d * d
			}
		}
	}
	for j := range scale {
		if observed[j] > 0 {
			scale[j] = math.Sqrt(scale[j] / float64(observed[j]))
		}
		if scale[j] < 1e-9 {
			scale[j] = 1
		}
	}
	coef := make([]float64, n)
	intercept := 0.0
	for epoch := 0; epoch < 500; epoch++ {
		gradient := make([]float64, n)
		gb := 0.0
		weightSum := 0.0
		for _, r := range rows {
			z := intercept
			for j, c := range cols {
				z += coef[j] * standardizedValue(r.Features, c, mean[j], scale[j])
			}
			p := sigmoid(z)
			w := r.TrainingWeight
			if w <= 0 {
				w = 1
			}
			e := (p - float64(r.LabelValue)) * w
			gb += e
			weightSum += w
			for j, c := range cols {
				gradient[j] += e * standardizedValue(r.Features, c, mean[j], scale[j])
			}
		}
		rate := 0.08 / math.Sqrt(1+float64(epoch)/50)
		intercept -= rate * gb / weightSum
		for j := range coef {
			coef[j] -= rate * (gradient[j]/weightSum + 0.01*coef[j])
		}
	}
	return logisticModel{HorizonMinutes: h, FeatureColumns: cols, Means: mean, Scales: scale, Coefficients: coef, Intercept: intercept, Threshold: 0.5}
}
func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

type scoredLabel struct {
	score     float64
	label     int
	threshold float64
}

func scoreRows(m logisticModel, rows []trainingMatrixRow) []scoredLabel {
	out := make([]scoredLabel, len(rows))
	for i, r := range rows {
		z := m.Intercept
		for j, c := range m.FeatureColumns {
			z += m.Coefficients[j] * standardizedValue(r.Features, c, m.Means[j], m.Scales[j])
		}
		out[i] = scoredLabel{score: sigmoid(z), label: r.LabelValue}
	}
	return out
}
func bestF1Threshold(scores []scoredLabel) float64 {
	bestT, best := 0.5, -1.0
	for i := 5; i <= 95; i++ {
		t := float64(i) / 100
		m := evaluateScores(scores, t)
		if m.F1 > best {
			best, bestT = m.F1, t
		}
	}
	return bestT
}
func evaluateScores(scores []scoredLabel, t float64) baselineMetrics {
	m := baselineMetrics{Count: len(scores)}
	tp, fp, fn := 0, 0, 0
	for _, s := range scores {
		threshold := t
		if threshold < 0 {
			threshold = s.threshold
		}
		if s.label == 1 {
			m.Positive++
		} else {
			m.Control++
		}
		m.Brier += (s.score - float64(s.label)) * (s.score - float64(s.label))
		if s.score >= threshold && s.label == 1 {
			tp++
		}
		if s.score >= threshold && s.label == 0 {
			fp++
		}
		if s.score < threshold && s.label == 1 {
			fn++
		}
	}
	if len(scores) > 0 {
		m.Brier /= float64(len(scores))
	}
	if tp+fp > 0 {
		m.Precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		m.Recall = float64(tp) / float64(tp+fn)
	}
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	m.ROCAUC = rankAUC(scores)
	m.PRAUC = prAUC(scores)
	return m
}
func rankAUC(scores []scoredLabel) float64 {
	sorted := append([]scoredLabel(nil), scores...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].score < sorted[j].score })
	rankSum := 0.0
	pos := 0
	for first := 0; first < len(sorted); {
		last := first + 1
		for last < len(sorted) && sorted[last].score == sorted[first].score {
			last++
		}
		averageRank := float64(first+1+last) / 2
		for index := first; index < last; index++ {
			if sorted[index].label == 1 {
				rankSum += averageRank
				pos++
			}
		}
		first = last
	}
	neg := len(sorted) - pos
	if pos == 0 || neg == 0 {
		return 0
	}
	return (rankSum - float64(pos*(pos+1))/2) / float64(pos*neg)
}

func standardizedValue(features map[string]float64, column string, mean, scale float64) float64 {
	value, exists := features[column]
	if !exists {
		return 0
	}
	return (value - mean) / scale
}
func prAUC(scores []scoredLabel) float64 {
	sorted := append([]scoredLabel(nil), scores...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].score > sorted[j].score })
	total := 0
	for _, s := range sorted {
		if s.label == 1 {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	tp, fp := 0, 0
	area, prevRecall := 0.0, 0.0
	for first := 0; first < len(sorted); {
		last := first + 1
		for last < len(sorted) && sorted[last].score == sorted[first].score {
			last++
		}
		for index := first; index < last; index++ {
			if sorted[index].label == 1 {
				tp++
			} else {
				fp++
			}
		}
		recall := float64(tp) / float64(total)
		precision := float64(tp) / float64(tp+fp)
		area += (recall - prevRecall) * precision
		prevRecall = recall
		first = last
	}
	return area
}
func macroTestMetrics(rows []baselineHorizonReport) baselineMetrics {
	m := baselineMetrics{}
	if len(rows) == 0 {
		return m
	}
	for _, r := range rows {
		m.Count += r.Test.Count
		m.Positive += r.Test.Positive
		m.Control += r.Test.Control
		m.ROCAUC += r.Test.ROCAUC
		m.PRAUC += r.Test.PRAUC
		m.Precision += r.Test.Precision
		m.Recall += r.Test.Recall
		m.F1 += r.Test.F1
		m.Brier += r.Test.Brier
	}
	n := float64(len(rows))
	m.ROCAUC /= n
	m.PRAUC /= n
	m.Precision /= n
	m.Recall /= n
	m.F1 /= n
	m.Brier /= n
	return m
}
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
