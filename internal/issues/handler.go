package issues

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type Handler struct {
	db      *storage.DB
	service *Service
}

const deprecatedSourceDifferenceIssue = "gpu_source_inconsistency"

func NewHandler(db *storage.DB) *Handler { return &Handler{db: db, service: NewService(db)} }
func NewHandlerWithService(db *storage.DB, service *Service) *Handler {
	return &Handler{db: db, service: service}
}

func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		issueError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.service.SyncDetectedIssues(); err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var total, resolved, ignored, remaining, activeDetection int64
	statisticsIssues(h.db).Count(&total)
	statisticsIssues(h.db).Where("status = ?", "resolved").Count(&resolved)
	statisticsIssues(h.db).Where("status = ?", "ignored").Count(&ignored)
	statisticsIssues(h.db).Where("status IN ?", []string{"open", "in_progress"}).Count(&remaining)
	statisticsIssues(h.db).Where("detection_state = ?", "active").Count(&activeDetection)
	byCategory, err := groupedCounts(statisticsIssues(h.db), "category")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolvedByCategory, err := groupedCounts(statisticsIssues(h.db).Where("status = ?", "resolved"), "category")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	remainingByCategory, err := groupedCounts(statisticsIssues(h.db).Where("status IN ?", []string{"open", "in_progress"}), "category")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	activeByCategory, err := groupedCounts(statisticsIssues(h.db).Where("detection_state = ?", "active"), "category")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byStatus, err := groupedCounts(statisticsIssues(h.db), "status")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bySeverity, err := groupedCounts(statisticsIssues(h.db), "severity")
	if err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	issueJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"discovered": total, "resolved": resolved, "remaining": remaining, "ignored": ignored, "active_detection": activeDetection,
		"by_category": byCategory, "resolved_by_category": resolvedByCategory, "remaining_by_category": remainingByCategory,
		"active_by_category": activeByCategory, "by_status": byStatus, "by_severity": bySeverity,
		"generated_at": time.Now().Format(time.RFC3339),
	}})
}

func (h *Handler) HandleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		issueError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.service.SyncDetectedIssues(); err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	limit, beforeID, err := issuePagination(r)
	if err != nil {
		issueError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := h.filteredQuery(r)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var rows []api.PlatformIssue
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var next uint
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	issueJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": total, "limit": limit, "has_more": hasMore, "next_before_id": next}})
}

func (h *Handler) filteredQuery(r *http.Request) *gorm.DB {
	query := visibleIssues(h.db)
	if strings.TrimSpace(r.URL.Query().Get("category")) == "" {
		query = query.Where("category <> ?", "hardware_fault")
	}
	for field, column := range map[string]string{"category": "category", "severity": "severity", "detection_state": "detection_state", "issue_type": "issue_type"} {
		if value := strings.TrimSpace(r.URL.Query().Get(field)); value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("status")); value == "remaining" {
		query = query.Where("status IN ?", []string{"open", "in_progress"})
	} else if value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + strings.ToLower(value) + "%"
		query = query.Where("LOWER(title) LIKE ? OR LOWER(description) LIKE ? OR LOWER(node_ip) LIKE ? OR LOWER(gpu_uuid) LIKE ? OR LOWER(entity_key) LIKE ?", like, like, like, like, like)
	}
	return query
}

func (h *Handler) HandleSubresource(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/issues/"), "/")
	parts := strings.Split(path, "/")
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || id == 0 {
		issueError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		h.detail(w, uint(id))
		return
	}
	if len(parts) == 2 && parts[1] == "resolution" && r.Method == http.MethodPost {
		h.addResolution(w, r, uint(id))
		return
	}
	issueError(w, http.StatusMethodNotAllowed, "unsupported issue operation")
}

func (h *Handler) detail(w http.ResponseWriter, id uint) {
	var issue api.PlatformIssue
	if err := h.db.First(&issue, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			issueError(w, http.StatusNotFound, "issue not found")
		} else {
			issueError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	var resolutions []api.IssueResolution
	if err := h.db.Where("issue_id = ?", id).Order("created_at DESC, id DESC").Find(&resolutions).Error; err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	issueJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"issue": issue, "resolutions": resolutions}})
}

type resolutionRequest struct {
	Status            string         `json:"status"`
	RootCause         string         `json:"root_cause"`
	Solution          string         `json:"solution"`
	ResolutionProcess string         `json:"resolution_process"`
	Result            string         `json:"result"`
	Evidence          api.StringList `json:"evidence"`
	Operator          string         `json:"operator"`
	TrainingEligible  bool           `json:"training_eligible"`
}

func (h *Handler) addResolution(w http.ResponseWriter, r *http.Request, id uint) {
	var request resolutionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		issueError(w, http.StatusBadRequest, "invalid resolution: "+err.Error())
		return
	}
	if !oneOfIssue(request.Status, "open", "in_progress", "resolved", "ignored") {
		issueError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if strings.TrimSpace(request.Operator) == "" {
		issueError(w, http.StatusBadRequest, "operator is required")
		return
	}
	if strings.TrimSpace(request.RootCause+request.Solution+request.ResolutionProcess+request.Result) == "" {
		issueError(w, http.StatusBadRequest, "resolution content is required")
		return
	}
	if request.TrainingEligible && (request.Status != "resolved" || strings.TrimSpace(request.RootCause) == "" || strings.TrimSpace(request.Solution) == "" || strings.TrimSpace(request.ResolutionProcess) == "" || strings.TrimSpace(request.Result) == "") {
		issueError(w, http.StatusBadRequest, "training-eligible resolution requires resolved status, cause, solution, process and result")
		return
	}
	var resolution api.IssueResolution
	err := h.db.Transaction(func(tx *gorm.DB) error {
		var issue api.PlatformIssue
		if err := tx.First(&issue, id).Error; err != nil {
			return err
		}
		resolution = api.IssueResolution{IssueID: id, Status: request.Status, RootCause: strings.TrimSpace(request.RootCause), Solution: strings.TrimSpace(request.Solution), ResolutionProcess: strings.TrimSpace(request.ResolutionProcess), Result: strings.TrimSpace(request.Result), Evidence: request.Evidence, Operator: strings.TrimSpace(request.Operator), TrainingEligible: request.TrainingEligible}
		if err := tx.Create(&resolution).Error; err != nil {
			return err
		}
		updates := map[string]any{"status": request.Status, "latest_resolution_id": resolution.ID}
		if request.Status == "resolved" {
			now := time.Now()
			updates["resolved_at"] = &now
		} else if request.Status == "open" || request.Status == "in_progress" {
			updates["resolved_at"] = nil
		}
		return tx.Model(&issue).Updates(updates).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			issueError(w, http.StatusNotFound, "issue not found")
		} else {
			issueError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	issueJSON(w, http.StatusCreated, map[string]any{"data": resolution})
}

func (h *Handler) HandleTrainingData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		issueError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	type trainingRow struct {
		api.IssueResolution
		Issue api.PlatformIssue `json:"issue"`
	}
	var resolutions []api.IssueResolution
	if err := h.db.Where("training_eligible = ?", true).Order("id ASC").Find(&resolutions).Error; err != nil {
		issueError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := make([]trainingRow, 0, len(resolutions))
	for _, resolution := range resolutions {
		var issue api.PlatformIssue
		if err := h.db.First(&issue, resolution.IssueID).Error; err != nil {
			continue
		}
		if issue.IssueType == deprecatedSourceDifferenceIssue {
			continue
		}
		rows = append(rows, trainingRow{IssueResolution: resolution, Issue: issue})
	}
	issueJSON(w, http.StatusOK, map[string]any{"schema_version": "atlas-issue-training-v1", "generated_at": time.Now().Format(time.RFC3339), "data": rows})
}

func visibleIssues(db *storage.DB) *gorm.DB {
	return db.Model(&api.PlatformIssue{}).Where("issue_type <> ?", deprecatedSourceDifferenceIssue)
}

func statisticsIssues(db *storage.DB) *gorm.DB {
	return visibleIssues(db).Where("category <> ?", "hardware_fault")
}

type groupedCount struct {
	Name  string
	Count int64
}

func groupedCounts(query *gorm.DB, column string) (map[string]int64, error) {
	var rows []groupedCount
	if err := query.Select(column + " AS name, count(*) AS count").Group(column).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.Name] = row.Count
	}
	return result, nil
}

func issuePagination(r *http.Request) (int, uint, error) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, 0, fmt.Errorf("invalid limit")
		}
		if value > 200 {
			value = 200
		}
		limit = value
	}
	var before uint
	if raw := strings.TrimSpace(r.URL.Query().Get("before_id")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 || uint64(uint(value)) != value {
			return 0, 0, fmt.Errorf("invalid before_id")
		}
		before = uint(value)
	}
	return limit, before, nil
}

func oneOfIssue(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
func issueJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func issueError(w http.ResponseWriter, status int, message string) {
	issueJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}
