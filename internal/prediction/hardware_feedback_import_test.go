package prediction

import (
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

const importLedgerFixture = `TEST-SERIAL-H100-90	示例机房	示例租户	示例H100集群	工程处理完单	系统	硬件	Xid 79	GD20260101010101	2026/01/28 13:54	2026/01/28 21:12		438	01月	现场断电静置后压测通过					h100gpu-90
TEST-SERIAL-H100-91	示例机房	示例租户	示例H100集群	工程处理完单	风扇	硬件	散热模块故障	GD20260707070707	2026/07/04 10:22	2026/07/04 10:41		19	07月
巡检发现风扇指示灯亮红，等待配件更换					h100gpu-91
TEST-SERIAL-H100-91	示例机房	示例租户	示例H100集群	厂商处理完单	风扇	硬件	短期不影响机器正常使用	GD20260707070707	2026/07/01 16:25	2026/07/13 12:28		17043	07月						h100gpu-91`

func TestParseHardwareFaultLedgerPreservesMultilineAndDuplicateTickets(t *testing.T) {
	rows := parseHardwareFaultLedger(importLedgerFixture)
	if len(rows) != 3 {
		t.Fatalf("parsed rows=%d want=3: %+v", len(rows), rows)
	}
	if rows[1].ticket != "GD20260707070707" || rows[2].ticket != rows[1].ticket {
		t.Fatalf("duplicate ticket was not preserved: %+v", rows)
	}
	if rows[1].hostname != "h100gpu-91" || rows[0].reportedAt != "2026/01/28 13:54" {
		t.Fatalf("source identity/time parse mismatch: %+v", rows)
	}
}

func TestHardwareFaultFeedbackImportPreviewAndCommitAreSafeAndIdempotent(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []api.GPUNode{
		{NodeIP: "10.0.0.90", Hostname: "h100gpu-90", AssetSerial: "TEST-SERIAL-H100-90"},
		{NodeIP: "10.0.0.91", Hostname: "h100gpu-91", AssetSerial: "TEST-SERIAL-H100-91"},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return time.Date(2026, 9, 7, 10, 0, 0, 0, feedbackLocalLocation) }
	input := HardwareFaultFeedbackImportInput{SourceSystem: "feishu-hardware-fault-ledger", Operator: "ops-a", RawText: importLedgerFixture}
	preview, err := service.ImportHardwareFaultFeedback(input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || preview.ParsedRows != 3 || preview.UniqueSourceRecords != 2 || preview.MappedRows != 3 || preview.DuplicateRows != 1 || preview.ImportableRows != 2 || preview.ImportedRows != 0 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	var count int64
	if err := db.Model(&api.HardwareFaultFeedbackRequest{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("preview must be read-only: count=%d err=%v", count, err)
	}
	input.Commit = true
	committed, err := service.ImportHardwareFaultFeedback(input)
	if err != nil {
		t.Fatal(err)
	}
	if committed.ImportedRows != 2 || committed.TrainingEligibleRows != 0 {
		t.Fatalf("unexpected commit: %+v", committed)
	}
	var rows []api.HardwareFaultFeedbackRequest
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("imported rows=%d want=2", len(rows))
	}
	for _, row := range rows {
		if row.TrainingEligible || row.TriageStatus != "pending_monitoring_confirmation" || row.SourceSystem == "" || row.SourceRowSHA256 == "" || row.SourceRawRecord == "" || row.SourceReportedAt == nil || row.SourceResolvedAt == nil {
			t.Fatalf("imported record lost safety/provenance fields: %+v", row)
		}
		if row.FaultTimePrecision != "window" || row.FaultWindowStartAt == nil || row.FaultWindowEndAt == nil || row.FaultWindowEndAt.Sub(*row.FaultWindowStartAt) != 12*time.Hour {
			t.Fatalf("reported time must remain an approximate monitoring-search window: %+v", row)
		}
	}
	candidate := rows[0]
	candidate.TrainingEligible = true
	if blockers := feedbackHardwareBlockers(candidate); !strings.Contains(strings.Join(blockers, " "), "monitoring confirmation is pending") {
		t.Fatalf("monitoring-pending feedback must stay blocked even if training eligibility is toggled: %v", blockers)
	}
	again, err := service.ImportHardwareFaultFeedback(input)
	if err != nil {
		t.Fatal(err)
	}
	if again.ImportedRows != 0 || again.ExistingRows != 3 {
		t.Fatalf("re-import must be idempotent: %+v", again)
	}
	if err := db.Model(&api.HardwareFaultFeedbackRequest{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("idempotent import changed row count: count=%d err=%v", count, err)
	}
}

func TestHardwareFaultFeedbackImportBlocksAssetConflict(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]api.GPUNode{
		{NodeIP: "10.0.0.1", Hostname: "different-host", AssetSerial: "TEST-SERIAL-H100-90"},
		{NodeIP: "10.0.0.2", Hostname: "h100gpu-90", AssetSerial: "other-serial"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	report, err := service.ImportHardwareFaultFeedback(HardwareFaultFeedbackImportInput{SourceSystem: "feishu", Operator: "ops", RawText: importLedgerFixture, Commit: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Records[0].AssetResolutionStatus != "conflict" || report.Records[0].Decision != "blocked" {
		t.Fatalf("serial/hostname conflict must block import: %+v", report.Records[0])
	}
}

func TestLedgerNodeResolutionFallsBackToCurrentGPUAssetSerial(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUAsset{
		AssetKey: "10.0.0.9-gpu-0", NodeIP: "10.0.0.9", GPUIndex: 0,
		HostSerial: "SERIAL-FROM-GPU-ASSET", CurrentNodeIdentity: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	nodeIP, status, note := service.resolveLedgerNode("SERIAL-FROM-GPU-ASSET", "")
	if nodeIP != "10.0.0.9" || status != "resolved" || !strings.Contains(note, "asset serial") {
		t.Fatalf("GPU asset serial fallback failed: ip=%s status=%s note=%s", nodeIP, status, note)
	}
}
