package assetaudit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testService(t *testing.T) *Service {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := raw.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := raw.AutoMigrate(&api.AssetAuditConfig{}, &api.AssetAuditRun{}, &api.InfrastructureAsset{}, &api.GPUNode{}, &api.CollectorTarget{}); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	service, err := NewService(&storage.DB{DB: raw}, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestParseSheetReference(t *testing.T) {
	ref, err := parseSheetReference("https://lexungroup.feishu.cn/sheets/Wghxs1fS6hgZJ4tMkHZca8UDnNf?sheet=iZKGk9")
	if err != nil {
		t.Fatalf("valid sheet URL rejected: %v", err)
	}
	if ref.SpreadsheetToken != "Wghxs1fS6hgZJ4tMkHZca8UDnNf" || ref.SheetID != "iZKGk9" {
		t.Fatalf("unexpected reference: %+v", ref)
	}

	for _, invalid := range []string{
		"https://lexungroup.feishu.cn/sheets/Wghxs1fS6hgZJ4tMkHZca8UDnNf",
		"https://lexungroup.feishu.cn/docs/Wghxs1fS6hgZJ4tMkHZca8UDnNf?sheet=iZKGk9",
		"https://lexungroup.feishu.cn/sheets/?sheet=iZKGk9",
	} {
		if _, err := parseSheetReference(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestParseDeliveryRowsResolvesColumnsAndDeliveryFlag(t *testing.T) {
	rows := [][]string{
		{"", "", "交付时间", "设备主机名", "系统主机名", "", "类型", "型号", "主机SN", "nodeSN", "", "", "", "OSIP", "掩码"},
		{"", "", "2026/10/9", "host-a", "sys-a", "", "GPU", "H100", "SN-A", "NSN-A", "", "", "", "10.80.1.10", "26"},
		{"", "", "待交付", "host-b", "sys-b", "", "GPU", "H100", "", "NSN-B", "", "", "", "not-an-ip", ""},
		{"", "", "2026/10/9", "", "sys-c", "", "CPU", "EPYC", "", "NSN-C", "", "", "", "10.80.1.12", "24"},
	}
	parsed, err := parseDeliveryRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 parsed rows, got %d", len(parsed))
	}

	first := parsed[0]
	if !first.Delivered || first.Hostname != "host-a" || first.IP != "10.80.1.10" || first.Mask != 26 || first.HostSN != "SN-A" || first.NodeSN != "NSN-A" || first.Model != "H100" || first.Index != 2 {
		t.Fatalf("unexpected first row: %+v", first)
	}
	if parsed[1].Delivered {
		t.Fatalf("row with a non-date delivery cell must not count as delivered: %+v", parsed[1])
	}
	if parsed[1].IP != "" {
		t.Fatalf("invalid IP must normalize to empty, got %q", parsed[1].IP)
	}
	if !parsed[2].Delivered || parsed[2].Hostname != "sys-c" || parsed[2].Mask != 24 {
		t.Fatalf("hostname must fall back to the system name and honour the declared mask: %+v", parsed[2])
	}

	if _, err := parseDeliveryRows(nil); err == nil {
		t.Fatal("an empty sheet must be rejected")
	}
}

func TestParseMask(t *testing.T) {
	cases := map[string]int{
		"24": 24, "/24": 24, "255.255.255.0": 24, "255.255.255.128": 25,
		"255.255.255.192": 26, "255.255.254.0": 23, "": 0, "abc": 0, "40": 0, "0": 0,
	}
	for input, want := range cases {
		if got := parseMask(input); got != want {
			t.Fatalf("parseMask(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestNormalizeSerial(t *testing.T) {
	if normalizeSerial("  sn-1a b ") != "SN1AB" {
		t.Fatal("serial normalization must strip spaces and hyphens and upper-case")
	}
	for _, blank := range []string{"", "-", "N/A", "null", "未知", "无"} {
		if normalizeSerial(blank) != "" {
			t.Fatalf("blank serial %q must normalize to empty", blank)
		}
	}
}

func indexOf(assets ...api.InfrastructureAsset) assetIndex {
	index := assetIndex{byIP: map[string]api.InfrastructureAsset{}, bySN: map[string]api.InfrastructureAsset{}}
	for _, asset := range assets {
		if asset.IPAddress != "" {
			index.byIP[asset.IPAddress] = asset
		}
		if serial := normalizeSerial(asset.SerialNumber); serial != "" {
			index.bySN[serial] = asset
		}
	}
	return index
}

func TestEvaluateDeliveredRowIssues(t *testing.T) {
	matched := deliveryRow{Index: 2, DeliveredAt: "2026/10/9", Hostname: "host-a", IP: "10.80.1.10", HostSN: "SN-A", Delivered: true}
	assets := indexOf(api.InfrastructureAsset{IPAddress: "10.80.1.10", SerialNumber: "SN-A", Present: true})
	if issues := evaluateDeliveredRow(matched, assets, nil); len(issues) != 0 {
		t.Fatalf("a fully consistent row must produce no findings: %+v", issues)
	}

	missing := evaluateDeliveredRow(matched, indexOf(), nil)
	if len(missing) != 1 || missing[0].Issue != "missing_asset" {
		t.Fatalf("expected missing_asset, got %+v", missing)
	}

	snMismatch := evaluateDeliveredRow(matched, indexOf(api.InfrastructureAsset{IPAddress: "10.80.1.10", SerialNumber: "SN-OTHER"}), nil)
	if len(snMismatch) != 1 || snMismatch[0].Issue != "sn_mismatch" {
		t.Fatalf("expected sn_mismatch, got %+v", snMismatch)
	}

	// The sheet SN resolves the asset, but the recorded address differs.
	ipMismatch := evaluateDeliveredRow(matched, indexOf(api.InfrastructureAsset{IPAddress: "10.80.1.99", SerialNumber: "SN-A"}), nil)
	if len(ipMismatch) != 1 || ipMismatch[0].Issue != "ip_mismatch" {
		t.Fatalf("expected ip_mismatch, got %+v", ipMismatch)
	}

	// Telemetry disagrees with the sheet even though the asset record matches.
	telemetry := evaluateDeliveredRow(matched, assets, map[string]api.GPUNode{
		"10.80.1.10": {NodeIP: "10.80.1.10", AssetSerial: "SN-LIVE"},
	})
	if len(telemetry) != 1 || telemetry[0].Issue != "sn_mismatch" || telemetry[0].TelemetrySN != "SN-LIVE" {
		t.Fatalf("expected telemetry sn_mismatch, got %+v", telemetry)
	}
}

func TestCollectSubnetsHonoursPerRowMask(t *testing.T) {
	rows := []deliveryRow{
		{Index: 2, IP: "10.80.1.10", Mask: 26, Delivered: true},
		{Index: 3, IP: "10.80.1.200", Mask: 26, Delivered: true},
		{Index: 4, IP: "10.80.2.10", Mask: 0, Delivered: true},
		{Index: 5, IP: "10.80.3.10", Mask: 24, Delivered: false},
	}
	subnets := collectSubnets(rows)
	got := map[string]bool{}
	for _, item := range subnets {
		got[item.cidr.String()] = true
	}
	if !got["10.80.1.0/26"] || !got["10.80.1.192/26"] || !got["10.80.2.0/24"] {
		t.Fatalf("expected the declared /26 networks and a /24 fallback, got %v", got)
	}
	if got["10.80.3.0/24"] {
		t.Fatalf("undelivered rows must not contribute a subnet: %v", got)
	}
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 distinct subnets, got %d: %v", len(got), got)
	}
}

func TestExclusionReasonRules(t *testing.T) {
	cases := []struct {
		systemName string
		deviceType string
		excluded   bool
	}{
		{"gpu-node-01", "GPU", false},
		{"gpfs-storage-1", "服务器", true},
		{"OBS-STORE-2", "服务器", true},
		{"node-obs-03", "GPU", true},
		{"switch-01", "网络设备", true},
		{"odf-node-1", "ODF", true},
		{"odf-node-1", "odf", true},
		{"core-sw", "核心交换机", false},
	}
	for _, testCase := range cases {
		reason := exclusionReason(testCase.systemName, testCase.deviceType)
		if got := reason != ""; got != testCase.excluded {
			t.Fatalf("exclusionReason(%q,%q)=%q, excluded=%v want %v", testCase.systemName, testCase.deviceType, reason, got, testCase.excluded)
		}
	}
}

func TestParseDeliveryRowsMarksOutOfScopeRows(t *testing.T) {
	rows := [][]string{
		{"", "", "交付时间", "设备主机名", "系统主机名", "", "类型", "型号", "主机SN", "nodeSN", "", "", "", "OSIP", "掩码"},
		{"", "", "2026/10/9", "host-a", "gpu-a", "", "GPU", "H100", "SN-A", "", "", "", "", "10.80.1.10", "24"},
		{"", "", "2026/10/9", "store-a", "gpfs-store-1", "", "服务器", "X", "SN-B", "", "", "", "", "10.80.2.10", "24"},
		{"", "", "2026/10/9", "sw-a", "sw-a", "", "网络设备", "SW", "SN-C", "", "", "", "", "10.80.3.10", "24"},
		{"", "", "2026/10/9", "odf-a", "odf-a", "", "ODF", "ST", "SN-D", "", "", "", "", "10.80.4.10", "24"},
	}
	parsed, err := parseDeliveryRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].Excluded {
		t.Fatalf("a GPU row must stay in scope: %+v", parsed[0])
	}
	if !parsed[1].Excluded || !strings.Contains(parsed[1].ExcludeReason, "存储") {
		t.Fatalf("a storage node must be excluded: %+v", parsed[1])
	}
	if !parsed[2].Excluded || !strings.Contains(parsed[2].ExcludeReason, "网络设备") {
		t.Fatalf("a network device must be excluded: %+v", parsed[2])
	}
	if !parsed[3].Excluded || !strings.Contains(parsed[3].ExcludeReason, "ODF") {
		t.Fatalf("an ODF node must be excluded: %+v", parsed[3])
	}

	got := map[string]bool{}
	for _, item := range collectSubnets(parsed) {
		got[item.cidr.String()] = true
	}
	if !got["10.80.1.0/24"] || got["10.80.2.0/24"] || got["10.80.3.0/24"] || got["10.80.4.0/24"] {
		t.Fatalf("only in-scope rows may contribute a subnet, got %v", got)
	}
}

// testServiceWithSheet serves a canned delivery sheet so the full check runs
// without touching Feishu.
func testServiceWithSheet(t *testing.T, rows [][]string) *Service {
	t.Helper()
	service := testService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"tok"}`))
		case strings.Contains(r.URL.Path, "/values/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok",
				"data": map[string]any{"valueRange": map[string]any{"values": rows}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	service.sheet = &sheetClient{client: server.Client(), baseURL: server.URL}
	return service
}

func TestRunDeliveryCheckSkipsOutOfScopeRows(t *testing.T) {
	rows := [][]string{
		{"", "", "交付时间", "设备主机名", "系统主机名", "", "类型", "型号", "主机SN", "nodeSN", "", "", "", "OSIP", "掩码"},
		{"", "", "2026/10/9", "host-a", "gpu-a", "", "GPU", "H100", "SN-A", "", "", "", "", "10.80.1.10", "24"},
		{"", "", "2026/10/9", "store-a", "obs-store-1", "", "服务器", "X", "SN-B", "", "", "", "", "10.80.2.10", "24"},
		{"", "", "2026/10/9", "sw-a", "sw-a", "", "网络设备", "SW", "SN-C", "", "", "", "", "10.80.3.10", "24"},
	}
	service := testServiceWithSheet(t, rows)
	if err := service.db.Create(&api.InfrastructureAsset{IPAddress: "10.80.1.10", SerialNumber: "SN-A", Present: true}).Error; err != nil {
		t.Fatal(err)
	}

	summary, findings, err := service.runDeliveryCheck(context.Background(),
		configForFetch{SheetURL: sheetURL, AppID: "cli_x", AppSecret: "secret"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SheetRows != 3 || summary.DeliveredRows != 3 || summary.ExcludedRows != 2 {
		t.Fatalf("unexpected summary counts: %+v", summary)
	}
	if summary.Matched != 1 || summary.MissingAsset != 0 || summary.IPMismatch != 0 || summary.SNMismatch != 0 {
		t.Fatalf("only the in-scope, consistent row must be counted: %+v", summary)
	}
	if len(findings) != 0 {
		t.Fatalf("excluded rows must not produce findings: %+v", findings)
	}
}

func TestAddSubnetKeepsSetOverlapFree(t *testing.T) {
	_, narrower, _ := net.ParseCIDR("10.0.1.0/24")
	subnets := addSubnet(nil, narrower)
	_, wider, _ := net.ParseCIDR("10.0.0.0/16")
	subnets = addSubnet(subnets, wider)
	if len(subnets) != 1 || subnets[0].cidr.String() != "10.0.0.0/16" {
		t.Fatalf("a wider subnet must replace the covered entries: %+v", subnets)
	}
	_, contained, _ := net.ParseCIDR("10.0.1.0/26")
	subnets = addSubnet(subnets, contained)
	if len(subnets) != 1 || subnets[0].cidr.String() != "10.0.0.0/16" {
		t.Fatalf("a subnet already covered must be dropped: %+v", subnets)
	}
}

func TestForEachHostSkipsNetworkAndBroadcast(t *testing.T) {
	_, network, _ := net.ParseCIDR("10.0.0.0/30")
	visited := make([]string, 0, 2)
	forEachHost(network, func(ip net.IP) bool {
		visited = append(visited, ip.String())
		return true
	})
	if strings.Join(visited, ",") != "10.0.0.1,10.0.0.2" {
		t.Fatalf("unexpected host walk: %v", visited)
	}
}

func TestCompareIPSortsNumerically(t *testing.T) {
	if compareIP("10.80.1.2", "10.80.1.10") >= 0 {
		t.Fatal("10.80.1.2 must sort before 10.80.1.10")
	}
	if compareIP("10.80.1.10", "10.80.1.10") != 0 {
		t.Fatal("identical addresses must compare equal")
	}
}

func TestDeliveryNotificationHighlightsFindings(t *testing.T) {
	clean := DeliverySummary{SheetRows: 10, DeliveredRows: 8, Matched: 8, GeneratedAt: time.Now()}
	title, template, content := deliveryNotification(clean, nil)
	if title != "Atlas 资产核查 · 已交付节点核对" || template != "green" || !strings.Contains(content, "全部已交付节点与资产记录一致") {
		t.Fatalf("unexpected clean notification: %q %q %q", title, template, content)
	}

	findings := []DeliveryFinding{{Row: 2, Hostname: "host-a", Issue: "missing_asset", DeliveredAt: "2026/10/9", Detail: "未找到匹配记录"}}
	sameTitle, dirtyTemplate, dirtyContent := deliveryNotification(DeliverySummary{GeneratedAt: time.Now()}, findings)
	if sameTitle != title || dirtyTemplate != "orange" || !strings.Contains(dirtyContent, "未录入资产") {
		t.Fatalf("unexpected mismatch notification: %q %q %q", sameTitle, dirtyTemplate, dirtyContent)
	}
}

func TestScanNotificationTruncatesAndFlagsLimit(t *testing.T) {
	title, template, content := scanNotification(ScanSummary{SubnetCount: 2, Unregistered: 1, GeneratedAt: time.Now()}, []ScanFinding{{IPAddress: "10.0.0.5", Subnet: "10.0.0.0/24"}})
	if title != "Atlas 资产核查 · 存活 IP 扫描" || template != "orange" || !strings.Contains(content, "10.0.0.5") {
		t.Fatalf("unexpected scan notification: %q %q %q", title, template, content)
	}

	_, truncatedTemplate, truncatedContent := scanNotification(ScanSummary{Truncated: true, Unregistered: 2, GeneratedAt: time.Now()}, nil)
	if truncatedTemplate != "yellow" || !strings.Contains(truncatedContent, "达到地址上限") {
		t.Fatalf("a truncated scan must be flagged: %q %q", truncatedTemplate, truncatedContent)
	}

	many := make([]ScanFinding, 0, maxNotificationFindings+5)
	for index := 0; index < maxNotificationFindings+5; index++ {
		many = append(many, ScanFinding{IPAddress: "10.0.0.1"})
	}
	_, _, longContent := scanNotification(ScanSummary{Unregistered: len(many), GeneratedAt: time.Now()}, many)
	if !strings.Contains(longContent, "其余") {
		t.Fatalf("a large finding set must be truncated with a pointer to Atlas: %s", longContent)
	}
}

func TestShouldNotifyGatesOnFindingsAndOptIn(t *testing.T) {
	cases := []struct {
		name     string
		row      api.AssetAuditConfig
		findings int
		want     bool
	}{
		{"disabled never notifies", api.AssetAuditConfig{NotifyEnabled: false, NotifyAlways: true}, 5, false},
		{"anomalies notify by default", api.AssetAuditConfig{NotifyEnabled: true}, 1, true},
		{"clean run stays silent by default", api.AssetAuditConfig{NotifyEnabled: true}, 0, false},
		{"opt-in notifies on a clean run", api.AssetAuditConfig{NotifyEnabled: true, NotifyAlways: true}, 0, true},
		{"opt-in still notifies on anomalies", api.AssetAuditConfig{NotifyEnabled: true, NotifyAlways: true}, 3, true},
	}
	for _, testCase := range cases {
		if got := shouldNotify(testCase.row, testCase.findings); got != testCase.want {
			t.Fatalf("%s: shouldNotify = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestNextDailyAtRollsToTomorrow(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 11, 3, 3, 0, 0, 0, location)
	next := nextDailyAt(before, 5, location)
	if next.Day() != 3 || next.Hour() != 5 {
		t.Fatalf("expected same-day 05:00, got %s", next)
	}
	after := time.Date(2026, 11, 3, 6, 0, 0, 0, location)
	if rolled := nextDailyAt(after, 5, location); rolled.Day() != 4 || rolled.Hour() != 5 {
		t.Fatalf("expected next-day 05:00, got %s", rolled)
	}
}

func TestEnsureAndSaveConfigPersistsEncryptedSecrets(t *testing.T) {
	service := testService(t)
	if err := service.EnsureConfig(testConfigOptions()); err != nil {
		t.Fatal(err)
	}
	row, err := service.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != singletonID || row.ScheduleHour != 5 || row.Timezone != "Asia/Shanghai" || row.ScanConcurrency != 1 || row.ScanTimeoutMS != 1000 {
		t.Fatalf("unexpected seeded configuration: %+v", row)
	}
	// A second seed must not overwrite the persisted row.
	if err := service.EnsureConfig(testConfigOptions()); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SaveConfig(ConfigInput{ScheduleHour: 26}); err == nil {
		t.Fatal("an out-of-range schedule hour must be rejected")
	}
	if _, err := service.SaveConfig(ConfigInput{Timezone: "Mars/Phobos"}); err == nil {
		t.Fatal("an unknown timezone must be rejected")
	}
	if _, err := service.SaveConfig(ConfigInput{DeliverySheetURL: "https://example.com/not-a-sheet"}); err == nil {
		t.Fatal("a non-sheet URL must be rejected")
	}
	if _, err := service.SaveConfig(ConfigInput{DeliveryEnabled: true, DeliverySheetURL: sheetURL, FeishuAppID: "cli_x"}); err == nil {
		t.Fatal("delivery check must require an app secret")
	}

	saved, err := service.SaveConfig(ConfigInput{
		Enabled: true, DeliveryEnabled: true, ScanEnabled: true, NotifyEnabled: true, NotifyAlways: true,
		DeliverySheetURL: sheetURL, FeishuAppID: "cli_x", FeishuAppSecret: "app-secret",
		NotifyWebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc", NotifySecret: "hook-secret",
		ScheduleHour: 5, Timezone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	view := configView(saved)
	if !view.FeishuAppSecretConfigured || !view.NotifySecretConfigured || !view.NotifyAlways || view.FeishuAppID != "cli_x" {
		t.Fatalf("secret presence must be reported without leaking values: %+v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "app-secret") || strings.Contains(string(encoded), "hook-secret") {
		t.Fatal("the configuration view must never serialize secret material")
	}

	appSecret, notifySecret, err := service.resolvedSecrets(saved)
	if err != nil {
		t.Fatal(err)
	}
	if appSecret != "app-secret" || notifySecret != "hook-secret" {
		t.Fatalf("secrets did not round-trip: %q %q", appSecret, notifySecret)
	}

	// A blank secret field preserves the stored ciphertext.
	again, err := service.SaveConfig(ConfigInput{DeliveryEnabled: true,
		DeliverySheetURL: sheetURL, FeishuAppID: "cli_x", ScheduleHour: 6, Timezone: "Asia/Shanghai", ScanTimeoutMS: 1500})
	if err != nil {
		t.Fatalf("re-save with a blank secret rejected: %v", err)
	}
	if string(again.FeishuAppSecretCT) != string(saved.FeishuAppSecretCT) {
		t.Fatal("a blank secret must not clear the stored ciphertext")
	}
	if again.ScheduleHour != 6 || again.ScanTimeoutMS != 1500 {
		t.Fatalf("non-secret fields must update: %+v", again)
	}
}

func TestHandlerRequiresManagementTokenForWrites(t *testing.T) {
	service := testService(t)
	handler := NewHandler(service, "tok", true)

	getRecorder := httptest.NewRecorder()
	handler.HandleConfig(getRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/asset-audit/config", nil))
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET must be open, got %d", getRecorder.Code)
	}

	body := `{"enabled":true,"delivery_check_enabled":false,"scan_enabled":true,"delivery_sheet_url":"` + sheetURL + `","schedule_hour":5,"timezone":"Asia/Shanghai"}`
	unauthorized := httptest.NewRecorder()
	handler.HandleConfig(unauthorized, httptest.NewRequest(http.MethodPut, "/api/v1/asset-audit/config", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("a write without the token must be rejected, got %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/v1/asset-audit/config", strings.NewReader(body))
	request.Header.Set("X-Atlas-Admin-Token", "tok")
	authorized := httptest.NewRecorder()
	handler.HandleConfig(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("an authorized write must succeed, got %d (%s)", authorized.Code, authorized.Body.String())
	}
}

const sheetURL = "https://lexungroup.feishu.cn/sheets/Wghxs1fS6hgZJ4tMkHZca8UDnNf?sheet=iZKGk9"

func testConfigOptions() config.AssetAuditOptions {
	return config.AssetAuditOptions{
		Enabled: true, DeliveryEnabled: false, ScanEnabled: true,
		DeliverySheetURL: sheetURL, NotifyEnabled: false, ScheduleHour: 5,
		Timezone: "Asia/Shanghai", ScanConcurrency: 1, ScanTimeoutMS: 1000,
	}
}
