import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';
import {
  Activity, AlertTriangle, BarChart3, Bell, BookOpen, BrainCircuit, CheckCircle2,
  ChevronLeft, ChevronRight, CircleGauge, ClipboardList, Command, Cpu, Database, Download, Eye, Filter, Gauge,
  Languages, Layers3, MemoryStick, Menu, Moon, Network,
  Palette, RefreshCw, Save, Search, Server, ShieldAlert, ShieldCheck, Sun,
  Thermometer, Trash2, X, Zap,
} from 'lucide-react';
import './App.css';

type PageId = 'overview' | 'gpus' | 'issues' | 'incidents' | 'validations' | 'quality' | 'models' | 'about';
type SubPage = { id: string; label: string };
type Tx = (zh: string, en: string) => string;

type Ingestion = {
  id: number; event_id: string; source: string; host: string; level: string; message: string;
  process_status: string; callback_status: string; raw_payload?: string;
  labels?: Record<string, string>; created_at: string; ai_report_status?: string;
  event_timestamp?: string; ai_report_summary?: string; ai_report_confidence?: number;
};
type IngestionMeta = {
  total: number; all_total: number; limit: number; has_more: boolean; next_before_id: number;
  latest_received_at?: string; received_5m: number; received_1h: number;
  source_mode: string; stream_status: string; server_time?: string;
};
type Failure = { id: number; source: string; level: string; message: string; process_status: string; callback_status: string; process_last_error: string; callback_last_error: string; updated_at: string };
type Report = { status: string; model: string; severity: string; summary: string; probable_causes?: string[]; recommended_actions?: string[]; confidence?: number };
type ServerStatus = { status: string; version: string; commit: string; build_time: string; server_time: string };
type PlatformConfig = { instance_name: string; product_name: string; product_tagline: string; environment: string; updated_at?: string };
type FreshnessSource = { status: string; observed_at?: string; age_seconds?: number; stale_after_seconds: number; source_mode?: string; collection_status?: string; collection_started_at?: string; collection_age_seconds?: number; collection_interval_seconds?: number; message?: string };
type DataFreshness = { overall_status: string; server_time: string; sources: Record<string, FreshnessSource> };
type GPUAsset = { id: number; asset_key: string; node_id: number; node_ip: string; gpu_index: number; gpu_uuid: string; device: string; model: string; model_name: string; pci_bus_id: string; host_serial: string; driver_version: string; state: string; sample_state: string; last_seen_at: string; last_synced_at: string };
type CollectorTarget = { id: number; job: string; instance: string; target_ip: string; node_ip: string; health: string; reason_code?: string; suppressed: boolean; suppression_reason?: string; last_error?: string; last_scrape_at?: string; last_synced_at: string };
type SyncRun = { id: number; task_type: string; status: string; node_count: number; gpu_count: number; known_uuid_count: number; target_count: number; change_count: number; started_at: string; finished_at?: string; error_message?: string };
type AssetChange = { id: number; sync_run_id: number; event_type: string; node_ip: string; asset_key: string; old_value: string; new_value: string; source: string; created_at: string };
type GPUHealthScore = { id: number; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; score: number | null; level: string; data_confidence: string; stability_score: number; memory_score: number; thermal_score: number; power_score: number; interconnect_score: number; performance_score: number; evidence: string[]; metric_sources?: Record<string, string>; sources_available?: string[]; fallback_metric_count?: number; consistency_issues?: string[]; consistency_issue_count?: number; rule_version: string; evaluated_at: string };
type HealthSummary = { total: number; scored: number; unknown: number; average_score: number; by_level: Record<string, number>; by_confidence: Record<string, number>; latest_run: { id: number; rule_version: string; status: string; rule_hit_count: number; finished_at?: string } | null };
type FaultEvent = { id: number; source: string; state: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; rule_code: string; domain: string; severity: string; evidence: string; observed_value: number; threshold: string; occurrence_count: number; rule_version: string; first_observed_at: string; last_observed_at: string; recovered_at?: string; issue_id?: number; workflow_status?: string; latest_resolution_id?: number };
type LocalizedText = { zh: string; en: string };
type FaultReportFinding = { code: string; severity: string; summary: LocalizedText; evidence_ids: string[] };
type FaultReportHypothesis = { code: string; status: string; title: LocalizedText; reason: LocalizedText; evidence_ids: string[] };
type FaultReportGap = { code: string; detail: LocalizedText };
type FaultReportTimeline = { at: string; evidence_id: string; label: LocalizedText };
type FaultAnalysisReport = {
  schema_version: string; report_version: string; generated_at: string; analysis_mode: string; event_id: number;
  title: LocalizedText; summary: LocalizedText; severity: string;
  affected_entity: { type: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string };
  timeline: FaultReportTimeline[]; findings: FaultReportFinding[]; hypotheses: FaultReportHypothesis[];
  missing_evidence: FaultReportGap[]; recommended_readonly_checks: LocalizedText[];
  operator_actions: LocalizedText[]; limitations: LocalizedText[]; no_action_executed: boolean;
};
type CursorMeta = { total: number; limit: number; has_more: boolean; next_before_id: number };
type FaultEventSummary = { total: number; open: number; by_state: Record<string, number>; open_by_severity: Record<string, number> };
type PlatformIssue = { id: number; issue_key: string; category: string; issue_type: string; title: string; description: string; entity_type: string; entity_key: string; node_ip: string; gpu_uuid: string; severity: string; status: string; detection_state: string; detection_source: string; source_record_id: number; first_detected_at: string; last_detected_at: string; source_recovered_at?: string; resolved_at?: string; latest_resolution_id: number };
type IssueResolution = { id: number; issue_id: number; status: string; root_cause: string; solution: string; resolution_process: string; result: string; evidence: string[]; operator: string; training_eligible: boolean; created_at: string };
type IssueDetail = { issue: PlatformIssue; resolutions: IssueResolution[] };
type IssueSummary = { discovered: number; resolved: number; remaining: number; ignored: number; active_detection: number; training_eligible: number; by_category: Record<string, number>; resolved_by_category: Record<string, number>; remaining_by_category: Record<string, number>; active_by_category: Record<string, number>; by_status: Record<string, number>; by_severity: Record<string, number>; generated_at: string };
type TelemetryQualityItem = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; sample_count_1h?: number; presence_ratio_1h?: number; sample_age_seconds?: number; uuid_presence_flap_count_1h?: number; metric_gap_max_seconds_1h?: number; target_scrape_success_ratio_5m?: number; target_scrape_samples_ratio_5m?: number; target_scrape_duration_ratio_5m?: number; observed_at: string };
type TelemetryQualitySummary = { total: number; by_status: Record<string, number>; average_presence_ratio_1h: number; max_sample_age_seconds: number; max_metric_gap_seconds_1h?: number; max_uuid_flap_count_1h?: number; min_target_scrape_success_ratio_5m?: number; min_target_scrape_samples_ratio_5m?: number; max_target_scrape_duration_ratio_5m?: number; feature_catalog_version: string; evaluation_run_id: number; evaluated_at?: string };
type NodeAccessCommand = { id: string; category: string; approval_class: string; planning_status: string; collection_mode: string; purpose: LocalizedText; preview: string };
type NodeAccessSkill = { id: string; version: string; class: string; status: string; purpose: LocalizedText };
type NodeCredentialStatus = { id: string; priority: number; username_masked: string; auth_type: string; secret_provider: string; enabled: boolean; secret_available: boolean; status: string };
type NodeAccessCheck = { id: number; node_ip: string; status: string; credential_profile_id?: string; attempts: string[]; alert_required: boolean; no_credential_disclosed: boolean; no_command_executed: boolean; started_at: string; finished_at: string };
type NodeAccessOverview = {
  skill_id: string; skill_version: string; status: string; enabled: boolean; execution_enabled: boolean;
  no_arbitrary_shell: boolean; no_change_executed: boolean; encryption_ready: boolean; management_ready: boolean; secure_write_only: boolean; insecure_http_allowed: boolean; connectivity_check_enabled: boolean; known_hosts_ready: boolean; default_read_only_collection: boolean;
  budget: { connect_timeout_seconds: number; command_timeout_seconds: number; max_output_bytes: number; max_concurrent_nodes: number; max_commands_per_node: number; max_log_lines: number; default_window_minutes: number };
  credential_profiles: NodeCredentialStatus[]; skills: NodeAccessSkill[]; commands: NodeAccessCommand[]; boundaries: LocalizedText[]; generated_at: string;
};
type DegradationSummary = { version: string; mode: string; evaluated_gpus: number; eligible_gpus: number; baseline_ready_gpus: number; historical_baseline_gpus: number; candidate_gpus: number; insufficient_gpus: number; minimum_utilization: number; ratio_threshold: number; freshness_sla_seconds: number; by_model: Record<string, number>; latest_observed_at?: string; evaluated_at: string };
type DegradationCandidate = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; metric: string; observed_value: number; baseline_value: number; performance_ratio: number; gpu_utilization: number; peer_count: number; baseline_id?: number; baseline_scope: string; baseline_maturity: string; data_confidence: string; evidence: string[]; recommended_action: string; feature_version: string; observed_at: string; evaluated_at: string };
type FleetSummary = {
  nodes: { total: number; by_state: Record<string, number> };
  gpus: { total: number; known_uuid: number; unknown_uuid: number; by_state: Record<string, number> };
  targets: { by_health: Record<string, number> };
  latest_sync: SyncRun | null;
};

const pageIcons = { overview: BarChart3, gpus: Cpu, issues: ClipboardList, incidents: Bell, validations: Gauge, quality: Database, models: BrainCircuit, about: BookOpen };
const pages: PageId[] = ['overview', 'gpus', 'incidents', 'validations', 'issues', 'quality', 'models', 'about'];
const pageCopy = (tx: Tx): Record<PageId, { label: string; group: string; title: string; desc: string }> => ({
  overview: { label: tx('集群总览', 'Overview'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 集群', 'GPU Fleet'), desc: tx('资产、状态、事件与交付进度', 'Assets, status, incidents and delivery') },
  gpus: { label: tx('GPU 资产', 'GPU Assets'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 资产', 'GPU Assets'), desc: tx('健康、异常、性能与取值来源', 'Health, anomaly, performance and source lineage') },
  issues: { label: tx('数据统计', 'Data Statistics'), group: tx('运行', 'OPERATIONS'), title: tx('数据问题统计与处置', 'Data Issue Analytics & Resolution'), desc: tx('数据、资产与可用性问题的发现、分类、状态及训练记录', 'Discovery, categories, status and training records for data, inventory and availability issues') },
  incidents: { label: tx('告警中心', 'Alert Center'), group: tx('运行', 'OPERATIONS'), title: tx('告警中心', 'Alert Center'), desc: tx('硬件告警、接收记录、详情与人工处置', 'Hardware alerts, ingestion records, details and operator resolution') },
  validations: { label: tx('性能验证', 'Validation'), group: tx('运行', 'OPERATIONS'), title: tx('性能验证', 'Performance Validation'), desc: tx('算力衰减检测与维护窗口复测', 'Degradation detection and maintenance validation') },
  quality: { label: tx('数据质量', 'Data Quality'), group: tx('系统', 'SYSTEM'), title: tx('数据质量', 'Data Quality'), desc: tx('采集覆盖、身份映射与能力矩阵', 'Coverage, identity mapping and capability matrix') },
  models: { label: tx('规则与模型', 'Rules & Models'), group: tx('系统', 'SYSTEM'), title: tx('规则与模型', 'Rules & Models'), desc: tx('健康评分、异常检测与故障预测', 'Health score, anomaly detection and failure prediction') },
  about: { label: tx('平台概览', 'Platform Overview'), group: tx('系统', 'SYSTEM'), title: tx('平台概览', 'Platform Overview'), desc: tx('定位、能力模块、版本与路线', 'Positioning, capability modules, versions and roadmap') },
});
const subPages = (page: PageId, tx: Tx): SubPage[] => ({
  overview: [],
  gpus: [{ id: 'health', label: tx('GPU 健康', 'GPU Health') }, { id: 'inventory', label: tx('资产清单', 'Inventory') }],
  issues: [],
  incidents: [{ id: 'hardware', label: tx('硬件事件', 'Hardware Events') }, { id: 'ingestion', label: tx('接收记录', 'Ingestion Records') }, { id: 'workflow', label: tx('处理流程', 'Workflow') }],
  validations: [{ id: 'degradation', label: tx('衰减检测', 'Degradation') }, { id: 'records', label: tx('验证记录', 'Records') }],
  quality: [{ id: 'targets', label: tx('采集覆盖', 'Target Coverage') }, { id: 'continuity', label: tx('指标连续性', 'Metric Continuity') }, { id: 'issues', label: tx('问题统计', 'Issue Statistics') }, { id: 'node-access', label: tx('节点访问', 'Node Access') }, { id: 'identity', label: tx('身份与带外', 'Identity & BMC') }, { id: 'audit', label: tx('同步审计', 'Sync Audit') }],
  models: [{ id: 'stack', label: tx('决策分层', 'Decision Stack') }, { id: 'algorithms', label: tx('算法与约束', 'Algorithms & Gates') }],
  about: [{ id: 'definition', label: tx('能力与里程碑', 'Capabilities & Milestones') }, { id: 'architecture', label: tx('架构与边界', 'Architecture & Boundaries') }, { id: 'settings', label: tx('平台配置', 'Platform Settings') }],
}[page]);

function time(value?: string, lang = 'zh-CN') {
  if (!value) return '—';
  const date = new Date(value); if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(lang, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date);
}
function compareIP(left: string, right: string) { const a = left.split('.').map(Number); const b = right.split('.').map(Number); for (let i = 0; i < Math.max(a.length, b.length); i += 1) { const delta = (a[i] || 0) - (b[i] || 0); if (delta) return delta; } return left.localeCompare(right); }
function tone(level?: string) { const v = (level || '').toLowerCase(); return v === 'critical' || v === 'error' ? 'danger' : v === 'warning' || v === 'attention' ? 'warning' : v === 'info' ? 'info' : v === 'healthy' || v === 'recovered' ? 'healthy' : 'neutral'; }
function issueTone(status?: string) { const value = (status || '').toLowerCase(); return value === 'resolved' ? 'healthy' : value === 'in_progress' ? 'info' : value === 'open' ? 'warning' : 'neutral'; }
function Badge({ value, kind = 'neutral' }: { value: string; kind?: string }) { return <span className={`badge ${kind}`}><i />{value}</span>; }
function Card({ children, className = '' }: { children: ReactNode; className?: string }) { return <section className={`card ${className}`}>{children}</section>; }
function CardHead({ code, title, action }: { code?: string; title: string; action?: ReactNode }) { return <div className="card-head"><div>{code && <span>{code}</span>}<h2>{title}</h2></div>{action}</div>; }

export default function App() {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const tx: Tx = (cn, en) => zh ? cn : en;
  const copy = pageCopy(tx);
  const initial = window.location.hash.replace('#/', '') as PageId;
  const [page, setPage] = useState<PageId>(pages.includes(initial) ? initial : 'overview');
  const [sidebar, setSidebar] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [ingestions, setIngestions] = useState<Ingestion[]>([]);
  const [ingestionMeta, setIngestionMeta] = useState<IngestionMeta>({ total: 0, all_total: 0, limit: 100, has_more: false, next_before_id: 0, received_5m: 0, received_1h: 0, source_mode: 'unknown', stream_status: 'empty' });
  const [ingestionBeforeID, setIngestionBeforeID] = useState(0);
  const [ingestionCursorHistory, setIngestionCursorHistory] = useState<number[]>([]);
  const [ingestionQuery, setIngestionQuery] = useState('');
  const [ingestionLevel, setIngestionLevel] = useState('');
  const [ingestionLoading, setIngestionLoading] = useState(false);
  const [ingestionRefresh, setIngestionRefresh] = useState(0);
  const [failures, setFailures] = useState<Failure[]>([]);
  const [status, setStatus] = useState<ServerStatus | null>(null);
  const [platformConfig, setPlatformConfig] = useState<PlatformConfig>({ instance_name: 'Atlas Cluster', product_name: 'ATLAS', product_tagline: 'GPU RELIABILITY', environment: 'LOCAL' });
  const [freshness, setFreshness] = useState<DataFreshness | null>(null);
  const [summary, setSummary] = useState<FleetSummary | null>(null);
  const [gpuAssets, setGPUAssets] = useState<GPUAsset[]>([]);
  const [targets, setTargets] = useState<CollectorTarget[]>([]);
  const [syncRuns, setSyncRuns] = useState<SyncRun[]>([]);
  const [assetChanges, setAssetChanges] = useState<AssetChange[]>([]);
  const [healthScores, setHealthScores] = useState<GPUHealthScore[]>([]);
  const [healthSummary, setHealthSummary] = useState<HealthSummary | null>(null);
  const [faultEvents, setFaultEvents] = useState<FaultEvent[]>([]);
  const [faultMeta, setFaultMeta] = useState<CursorMeta>({ total: 0, limit: 100, has_more: false, next_before_id: 0 });
  const [faultBeforeID, setFaultBeforeID] = useState(0);
  const [faultCursorHistory, setFaultCursorHistory] = useState<number[]>([]);
  const [faultLoading, setFaultLoading] = useState(false);
  const [faultSummary, setFaultSummary] = useState<FaultEventSummary | null>(null);
  const [inventoryError, setInventoryError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<number | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [level, setLevel] = useState('');
  const [issueSummary, setIssueSummary] = useState<IssueSummary | null>(null);
  const [issues, setIssues] = useState<PlatformIssue[]>([]);
  const [issueMeta, setIssueMeta] = useState<CursorMeta>({ total: 0, limit: 50, has_more: false, next_before_id: 0 });
  const [issueBeforeID, setIssueBeforeID] = useState(0);
  const [issueCursorHistory, setIssueCursorHistory] = useState<number[]>([]);
  const [issueCategory, setIssueCategory] = useState('');
  const [issueStatus, setIssueStatus] = useState('');
  const [issueQuery, setIssueQuery] = useState('');
  const [issueLoading, setIssueLoading] = useState(false);
  const [selectedIssueID, setSelectedIssueID] = useState<number | null>(null);
  const [issueDetail, setIssueDetail] = useState<IssueDetail | null>(null);
  const [issueRefresh, setIssueRefresh] = useState(0);
  const [selectedFaultReportID, setSelectedFaultReportID] = useState<number | null>(null);
  const [faultReport, setFaultReport] = useState<FaultAnalysisReport | null>(null);
  const [faultReportLoading, setFaultReportLoading] = useState(false);
  const [faultReportError, setFaultReportError] = useState('');
  const [telemetryQuality, setTelemetryQuality] = useState<TelemetryQualityItem[]>([]);
  const [telemetryQualitySummary, setTelemetryQualitySummary] = useState<TelemetryQualitySummary | null>(null);
  const [degradationSummary, setDegradationSummary] = useState<DegradationSummary | null>(null);
  const [degradationCandidates, setDegradationCandidates] = useState<DegradationCandidate[]>([]);
  const [nodeAccess, setNodeAccess] = useState<NodeAccessOverview | null>(null);
  const [subPage, setSubPage] = useState<Record<PageId, string>>({ overview: '', gpus: 'health', issues: '', incidents: 'hardware', validations: 'degradation', quality: 'targets', models: 'stack', about: 'definition' });

  const navigate = (id: PageId) => { setPage(id); setSidebar(false); window.location.hash = `/${id}`; window.scrollTo(0, 0); };
  const load = async () => {
    setLoading(true);
    try {
      const [s, df, f, fs, ga, ct, sr, ac, hs, hg, fes, is, pc, ds, dc, tq, na] = await Promise.all([
        fetch('/api/v1/status'), fetch('/api/v1/data-freshness'), fetch('/api/v1/alerts/failures?limit=8'),
        fetch('/api/v1/fleet/summary'), fetch('/api/v1/gpus?limit=2000'), fetch('/api/v1/targets?limit=2000'),
        fetch('/api/v1/sync-runs?limit=20'), fetch('/api/v1/inventory/changes?limit=50'),
        fetch('/api/v1/health/summary'), fetch('/api/v1/health/gpus?limit=1000'), fetch('/api/v1/fault-events/summary'), fetch('/api/v1/issues/summary'),
        fetch('/api/v1/platform-config'),
        fetch('/api/v1/degradation/summary'), fetch('/api/v1/degradation/candidates'), fetch('/api/v1/health/telemetry-quality'),
        fetch('/api/v1/node-access/overview'),
      ]);
      if (s.ok) setStatus(await s.json());
      if (df.ok) setFreshness(await df.json()); else setFreshness({ overall_status: 'error', server_time: new Date().toISOString(), sources: {} });
      if (f.ok) { const data = await f.json(); setFailures(Array.isArray(data.items) ? data.items : []); }
      if (fs.ok && ga.ok && ct.ok) {
        const [fleetData, gpuData, targetData] = await Promise.all([fs.json(), ga.json(), ct.json()]);
        setSummary(fleetData.data || null);
        setGPUAssets(Array.isArray(gpuData.data) ? gpuData.data : []);
        setTargets(Array.isArray(targetData.data) ? targetData.data : []);
        setInventoryError(false);
      } else setInventoryError(true);
      if (sr.ok) { const data = await sr.json(); setSyncRuns(Array.isArray(data.data) ? data.data : []); }
      if (ac.ok) { const data = await ac.json(); setAssetChanges(Array.isArray(data.data) ? data.data : []); }
      if (hs.ok) { const data = await hs.json(); setHealthSummary(data.data || null); }
      if (hg.ok) { const data = await hg.json(); setHealthScores(Array.isArray(data.data) ? data.data : []); }
      if (fes.ok) { const data = await fes.json(); setFaultSummary(data.data || null); }
      if (is.ok) { const data = await is.json(); setIssueSummary(data.data || null); }
      if (pc.ok) { const data = await pc.json(); if (data.data) setPlatformConfig(data.data); }
      if (ds.ok) { const data = await ds.json(); setDegradationSummary(data.data || null); }
      if (dc.ok) { const data = await dc.json(); setDegradationCandidates(Array.isArray(data.data) ? data.data : []); }
      if (tq.ok) { const data = await tq.json(); setTelemetryQuality(Array.isArray(data.data) ? data.data : []); setTelemetryQualitySummary(data.summary || null); }
      if (na.ok) { const data = await na.json(); setNodeAccess(data.data || null); }
    } catch { setInventoryError(true); } finally { setLoading(false); }
  };
  useEffect(() => { load(); const id = window.setInterval(load, 30000); return () => window.clearInterval(id); }, []);
  useEffect(() => { document.title = `${platformConfig.instance_name} · ${platformConfig.product_name}`; }, [platformConfig]);
  useEffect(() => {
    let cancelled = false;
    const loadPage = async () => {
      setIngestionLoading(true);
      try {
        const params = new URLSearchParams({ limit: '100' });
        if (ingestionBeforeID > 0) params.set('before_id', String(ingestionBeforeID));
        if (ingestionQuery.trim()) params.set('query', ingestionQuery.trim());
        if (ingestionLevel) params.set('level', ingestionLevel);
        const response = await fetch(`/api/v1/alerts/ingestions?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const data = await response.json();
        if (cancelled) return;
        setIngestions(Array.isArray(data.items) ? data.items : []);
        setIngestionMeta({
          total: Number(data.total || 0), all_total: Number(data.all_total ?? data.total ?? 0), limit: Number(data.limit || 100),
          has_more: Boolean(data.has_more), next_before_id: Number(data.next_before_id || 0),
          latest_received_at: data.latest_received_at || undefined, received_5m: Number(data.received_5m || 0), received_1h: Number(data.received_1h || 0),
          source_mode: String(data.source_mode || 'unknown'), stream_status: String(data.stream_status || 'unknown'), server_time: data.server_time || undefined,
        });
      } catch {
        if (!cancelled) setIngestionMeta(current => ({ ...current, stream_status: 'error' }));
      } finally {
        if (!cancelled) setIngestionLoading(false);
      }
    };
    const debounce = window.setTimeout(loadPage, ingestionQuery ? 250 : 0);
    const interval = window.setInterval(loadPage, 30000);
    return () => { cancelled = true; window.clearTimeout(debounce); window.clearInterval(interval); };
  }, [ingestionBeforeID, ingestionQuery, ingestionLevel, ingestionRefresh]);
  useEffect(() => { setIngestionBeforeID(0); setIngestionCursorHistory([]); }, [ingestionQuery, ingestionLevel]);
  useEffect(() => {
    let cancelled = false;
    const loadFaultPage = async () => {
      setFaultLoading(true);
      try {
        const params = new URLSearchParams({ limit: '100' });
        if (faultBeforeID > 0) params.set('before_id', String(faultBeforeID));
        if (query.trim()) params.set('q', query.trim());
        if (level) params.set('severity', level);
        const response = await fetch(`/api/v1/fault-events?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json();
        if (cancelled) return;
        setFaultEvents(Array.isArray(payload.data) ? payload.data : []);
        setFaultMeta({ total: Number(payload.meta?.total || 0), limit: Number(payload.meta?.limit || 100), has_more: Boolean(payload.meta?.has_more), next_before_id: Number(payload.meta?.next_before_id || 0) });
      } catch { if (!cancelled) setFaultMeta(current => ({ ...current, has_more: false })); }
      finally { if (!cancelled) setFaultLoading(false); }
    };
    const debounce = window.setTimeout(loadFaultPage, query ? 250 : 0);
    return () => { cancelled = true; window.clearTimeout(debounce); };
  }, [faultBeforeID, query, level, ingestionRefresh, issueRefresh]);
  useEffect(() => { setFaultBeforeID(0); setFaultCursorHistory([]); }, [query, level]);
  useEffect(() => {
    let cancelled = false;
    const loadIssues = async () => {
      setIssueLoading(true);
      try {
        const params = new URLSearchParams({ limit: '50' });
        if (issueBeforeID > 0) params.set('before_id', String(issueBeforeID));
        if (issueCategory) params.set('category', issueCategory);
        if (issueStatus) params.set('status', issueStatus);
        if (issueQuery.trim()) params.set('q', issueQuery.trim());
        const response = await fetch(`/api/v1/issues?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json();
        if (cancelled) return;
        setIssues(Array.isArray(payload.data) ? payload.data : []);
        setIssueMeta({ total: Number(payload.meta?.total || 0), limit: Number(payload.meta?.limit || 50), has_more: Boolean(payload.meta?.has_more), next_before_id: Number(payload.meta?.next_before_id || 0) });
      } catch { if (!cancelled) setIssueMeta(current => ({ ...current, has_more: false })); }
      finally { if (!cancelled) setIssueLoading(false); }
    };
    const debounce = window.setTimeout(loadIssues, issueQuery ? 250 : 0);
    return () => { cancelled = true; window.clearTimeout(debounce); };
  }, [issueBeforeID, issueCategory, issueStatus, issueQuery, issueRefresh]);
  useEffect(() => { setIssueBeforeID(0); setIssueCursorHistory([]); }, [issueCategory, issueStatus, issueQuery]);
  useEffect(() => {
    if (!selectedIssueID) { setIssueDetail(null); return; }
    let cancelled = false;
    fetch(`/api/v1/issues/${selectedIssueID}`).then(response => response.ok ? response.json() : null).then(payload => { if (!cancelled) setIssueDetail(payload?.data || null); }).catch(() => { if (!cancelled) setIssueDetail(null); });
    return () => { cancelled = true; };
  }, [selectedIssueID, issueRefresh]);
  useEffect(() => {
    if (!selectedFaultReportID) { setFaultReport(null); setFaultReportError(''); setFaultReportLoading(false); return; }
    let cancelled = false;
    setFaultReport(null); setFaultReportError(''); setFaultReportLoading(true);
    fetch(`/api/v1/fault-events/${selectedFaultReportID}/report`)
      .then(async response => {
        const payload = await response.json().catch(() => null);
        if (!response.ok) throw new Error(payload?.error?.message || `HTTP ${response.status}`);
        return payload?.data as FaultAnalysisReport;
      })
      .then(payload => { if (!cancelled) setFaultReport(payload); })
      .catch(reason => { if (!cancelled) setFaultReportError(reason instanceof Error ? reason.message : String(reason)); })
      .finally(() => { if (!cancelled) setFaultReportLoading(false); });
    return () => { cancelled = true; };
  }, [selectedFaultReportID]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => { if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setSearchOpen(true); } if (e.key === '/' && !['INPUT', 'TEXTAREA'].includes((e.target as HTMLElement).tagName)) { e.preventDefault(); setSearchOpen(true); } };
    window.addEventListener('keydown', key); return () => window.removeEventListener('keydown', key);
  }, []);
  useEffect(() => {
    if (!selected) { setReport(null); return; }
    fetch(`/api/v1/alerts/ingestions/${selected}/analysis`).then(r => r.ok ? r.json() : null).then(setReport).catch(() => setReport(null));
  }, [selected]);

  const selectedItem = ingestions.find(x => x.id === selected) || null;
  const fleetModels = useMemo(() => {
    const counts = new Map<string, number>();
    gpuAssets.forEach(x => { const name = x.model_name || x.model || 'UNKNOWN'; counts.set(name, (counts.get(name) || 0) + 1); });
    return [...counts.entries()].map(([model, count]) => ({ model, short: model.replace(/^NVIDIA\s+/i, '').replace('GeForce ', ''), count })).sort((a, b) => b.count - a.count);
  }, [gpuAssets]);
  const filtered = ingestions;
  const openFaults = faultEvents.filter(x => x.state === 'open');
  const hosts = new Set(openFaults.map(x => x.node_ip).filter(Boolean)).size;
  const lang = zh ? 'zh-CN' : 'en-US';
  const freshnessLatest = useMemo(() => {
    const values = Object.values(freshness?.sources || {}).map(x => x.observed_at).filter((x): x is string => Boolean(x));
    return values.sort((a, b) => new Date(b).getTime() - new Date(a).getTime())[0];
  }, [freshness]);
  const freshnessStatus = (freshness?.overall_status || (loading ? 'syncing' : 'unknown')).toUpperCase();
  const freshnessTone = freshnessStatus === 'FRESH' ? 'healthy' : freshnessStatus === 'PARTIAL' || freshnessStatus === 'SNAPSHOT' ? 'info' : freshnessStatus === 'STALE' || freshnessStatus === 'OVERDUE' ? 'warning' : freshnessStatus === 'ERROR' ? 'danger' : 'neutral';
  const collectionSources = Object.entries(freshness?.sources || {}).filter(([, source]) => source.collection_status === 'running' || source.collection_status === 'overdue');
  const overdueSources = collectionSources.filter(([, source]) => source.collection_status === 'overdue');
  const currentSubPages = subPages(page, tx);
  const nextIngestionPage = () => {
    if (!ingestionMeta.has_more || !ingestionMeta.next_before_id) return;
    setIngestionCursorHistory(current => [...current, ingestionBeforeID]);
    setIngestionBeforeID(ingestionMeta.next_before_id);
  };
  const previousIngestionPage = () => {
    if (ingestionCursorHistory.length === 0) return;
    const previous = ingestionCursorHistory[ingestionCursorHistory.length - 1];
    setIngestionCursorHistory(current => current.slice(0, -1));
    setIngestionBeforeID(previous);
  };
  const nextFaultPage = () => { if (faultMeta.has_more && faultMeta.next_before_id) { setFaultCursorHistory(current => [...current, faultBeforeID]); setFaultBeforeID(faultMeta.next_before_id); } };
  const previousFaultPage = () => { if (faultCursorHistory.length) { const previous = faultCursorHistory[faultCursorHistory.length - 1]; setFaultCursorHistory(current => current.slice(0, -1)); setFaultBeforeID(previous); } };
  const nextIssuePage = () => { if (issueMeta.has_more && issueMeta.next_before_id) { setIssueCursorHistory(current => [...current, issueBeforeID]); setIssueBeforeID(issueMeta.next_before_id); } };
  const previousIssuePage = () => { if (issueCursorHistory.length) { const previous = issueCursorHistory[issueCursorHistory.length - 1]; setIssueCursorHistory(current => current.slice(0, -1)); setIssueBeforeID(previous); } };
  const refresh = () => { void load(); setIngestionRefresh(value => value + 1); setIssueRefresh(value => value + 1); };

  return <div className="app">
    <aside className={`sidebar ${sidebar ? 'open' : ''}`}>
      <div className="brand"><span className="brand-icon"><Layers3 size={19} /></span><div><b>{platformConfig.instance_name}</b><small>{platformConfig.product_name}</small></div><button className="mobile-only icon-btn" onClick={() => setSidebar(false)}><X size={17} /></button></div>
      <button className="cluster-switch" onClick={() => { navigate('about'); setSubPage(current => ({ ...current, about: 'settings' })); }}><span><i /> {platformConfig.environment}</span><small>{platformConfig.product_tagline}</small><ChevronRight size={14} /></button>
      <nav>{['运行', '系统'].map(group => <div className="nav-group" key={group}><label>{tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')}</label>{pages.filter(id => copy[id].group === tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')).map(id => { const Icon = pageIcons[id]; const count = id === 'issues' ? issueSummary?.remaining : id === 'incidents' ? faultSummary?.open : id === 'quality' ? issueSummary?.remaining_by_category?.data_quality : 0; return <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={16} /><span>{copy[id].label}</span>{(count || 0) > 0 && <em>{count}</em>}</button>; })}</div>)}</nav>
      <div className="sidebar-footer"><span><i className={status?.status === 'ok' ? 'online' : ''} />API {status?.status === 'ok' ? 'ONLINE' : 'WAITING'}</span><small>{status?.version || 'dev'} · {status?.commit || 'local'}</small></div>
    </aside>
    {sidebar && <button className="scrim" onClick={() => setSidebar(false)} />}
    <main>
      <header className="topbar">
        <button className="mobile-only icon-btn" onClick={() => setSidebar(true)}><Menu size={18} /></button>
        <div className="crumb"><span>{platformConfig.instance_name}</span><ChevronRight size={13} /><b>{copy[page].label}</b></div>
        <button className="global-search" onClick={() => setSearchOpen(true)}><Search size={15} /><span>{tx('搜索资源', 'Search resources')}</span><kbd>⌘ K</kbd></button>
        <div className="top-actions"><LanguageButton i18n={i18n} zh={zh} /><ThemeButton tx={tx} /><button className="icon-btn" onClick={refresh} title={tx('刷新', 'Refresh')}><RefreshCw size={16} className={loading || ingestionLoading ? 'spin' : ''} /></button></div>
      </header>
      <div className="content">
        <div className="page-head"><div><h1>{copy[page].title}</h1><p>{copy[page].desc}</p></div><div className="page-meta"><Badge value={platformConfig.environment} kind="info" /><Badge value={`DATA ${freshnessStatus}`} kind={freshnessTone} />{collectionSources.length > 0 && <Badge value={overdueSources.length > 0 ? tx('采集超时', 'COLLECTION OVERDUE') : tx('采集中', 'COLLECTING')} kind={overdueSources.length > 0 ? 'warning' : 'info'} />}<span title={tx('源数据最新观测时间；不是页面刷新时间', 'Latest source observation; not the page refresh time')}>{freshnessLatest ? time(freshnessLatest, lang) : '—'}</span></div></div>
        {overdueSources.length > 0 && <div className="collection-alert"><AlertTriangle size={17} /><div><b>{tx('监控数据超过 10 分钟仍未获取完成', 'Monitoring collection has not completed within 10 minutes')}</b><span>{overdueSources.map(([name, source]) => `${name.toUpperCase()} ${Math.floor((source.collection_age_seconds || 0) / 60)}m`).join(' · ')}</span></div></div>}
        {currentSubPages.length > 0 && <nav className="subnav" aria-label={tx('页面分区', 'Page sections')}>{currentSubPages.map(item => <button key={item.id} className={subPage[page] === item.id ? 'active' : ''} onClick={() => setSubPage(current => ({ ...current, [page]: item.id }))}>{item.label}</button>)}</nav>}
        <AnimatePresence mode="wait"><motion.div key={page} className={page === 'overview' ? 'overview-page' : 'secondary-page'} initial={{ opacity: 0, y: 5 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: .12 }}>
          {page === 'overview' && <Overview tx={tx} faults={openFaults} faultSummary={faultSummary} hosts={hosts} loading={loading} summary={summary} models={fleetModels} inventoryError={inventoryError} navigate={navigate} />}
          {page === 'gpus' && <Gpus tx={tx} view={subPage.gpus} assets={gpuAssets} models={fleetModels} loading={loading} inventoryError={inventoryError} healthScores={healthScores} healthSummary={healthSummary} lang={lang} />}
          {page === 'issues' && <Issues tx={tx} summary={issueSummary} rows={issues} meta={issueMeta} page={issueCursorHistory.length + 1} loading={issueLoading} category={issueCategory} setCategory={setIssueCategory} status={issueStatus} setStatus={setIssueStatus} query={issueQuery} setQuery={setIssueQuery} previousPage={previousIssuePage} nextPage={nextIssuePage} select={setSelectedIssueID} lang={lang} />}
          {page === 'incidents' && <Incidents tx={tx} view={subPage.incidents} rows={filtered} ingestionMeta={ingestionMeta} ingestionPage={ingestionCursorHistory.length + 1} ingestionLoading={ingestionLoading} previousIngestionPage={previousIngestionPage} nextIngestionPage={nextIngestionPage} faultRows={faultEvents} faultMeta={faultMeta} faultPage={faultCursorHistory.length + 1} faultLoading={faultLoading} previousFaultPage={previousFaultPage} nextFaultPage={nextFaultPage} query={subPage.incidents === 'ingestion' ? ingestionQuery : query} setQuery={subPage.incidents === 'ingestion' ? setIngestionQuery : setQuery} level={subPage.incidents === 'ingestion' ? ingestionLevel : level} setLevel={subPage.incidents === 'ingestion' ? setIngestionLevel : setLevel} failures={failures} select={setSelected} selectIssue={setSelectedIssueID} selectReport={setSelectedFaultReportID} lang={lang} />}
          {page === 'validations' && <Validations tx={tx} view={subPage.validations} summary={degradationSummary} candidates={degradationCandidates} lang={lang} />}
          {page === 'quality' && <Quality tx={tx} view={subPage.quality} targets={targets} summary={summary} issueSummary={issueSummary} inventoryError={inventoryError} syncRuns={syncRuns} assetChanges={assetChanges} telemetry={telemetryQuality} telemetrySummary={telemetryQualitySummary} nodeAccess={nodeAccess} reloadNodeAccess={() => void load()} openIssues={() => { setIssueCategory('data_quality'); setIssueStatus(''); setIssueQuery(''); setIssueBeforeID(0); setIssueCursorHistory([]); navigate('issues'); }} lang={lang} />}
          {page === 'models' && <Models tx={tx} view={subPage.models} />}
          {page === 'about' && <About tx={tx} view={subPage.about} platformConfig={platformConfig} onPlatformConfig={setPlatformConfig} />}
        </motion.div></AnimatePresence>
      </div>
    </main>
    <AnimatePresence>{searchOpen && <GlobalSearch tx={tx} query={query} setQuery={setQuery} pagesCopy={copy} items={ingestions} assets={gpuAssets} close={() => setSearchOpen(false)} navigate={navigate} select={id => { setSelected(id); navigate('incidents'); setSearchOpen(false); }} />}</AnimatePresence>
    <AnimatePresence>{selectedItem && <Drawer tx={tx} item={selectedItem} report={report} lang={lang} close={() => setSelected(null)} />}</AnimatePresence>
    <AnimatePresence>{selectedIssueID && issueDetail && <IssueDrawer tx={tx} detail={issueDetail} lang={lang} close={() => setSelectedIssueID(null)} saved={() => { setIssueRefresh(value => value + 1); void load(); }} />}</AnimatePresence>
    <AnimatePresence>{selectedFaultReportID && <FaultReportDrawer tx={tx} report={faultReport} loading={faultReportLoading} error={faultReportError} lang={lang} close={() => setSelectedFaultReportID(null)} />}</AnimatePresence>
  </div>;
}

function LanguageButton({ i18n, zh }: { i18n: { changeLanguage: (lang: string) => Promise<unknown> }; zh: boolean }) {
  const change = () => { const next = zh ? 'en' : 'zh'; localStorage.setItem('atlas_lang', next); void i18n.changeLanguage(next); };
  return <button className="icon-btn lang-btn" onClick={change} title={zh ? 'English' : '中文'}><Languages size={16} /><span>{zh ? '中' : 'EN'}</span></button>;
}
function ThemeButton({ tx }: { tx: Tx }) {
  const { theme, setTheme } = useTheme(); const [open, setOpen] = useState(false);
  const themes = [{ id: 'system', icon: CircleGauge, label: tx('跟随系统', 'System') }, { id: 'light', icon: Sun, label: tx('亮色', 'Light') }, { id: 'dark', icon: Moon, label: tx('暗色', 'Dark') }, { id: 'color', icon: Palette, label: tx('彩色', 'Color') }];
  return <div className="theme-menu"><button className="icon-btn" onClick={() => setOpen(!open)} title={tx('外观', 'Appearance')}><Palette size={16} /></button>{open && <div className="popover">{themes.map(x => <button key={x.id} className={theme === x.id ? 'active' : ''} onClick={() => { setTheme(x.id); setOpen(false); }}><x.icon size={15} />{x.label}{theme === x.id && <CheckCircle2 size={14} />}</button>)}</div>}</div>;
}

function Overview({ tx, faults, faultSummary, hosts, loading, summary, models, inventoryError, navigate }: { tx: Tx; faults: FaultEvent[]; faultSummary: FaultEventSummary | null; hosts: number; loading: boolean; summary: FleetSummary | null; models: { model: string; short: string; count: number }[]; inventoryError: boolean; navigate: (p: PageId) => void }) {
  const gpuTotal = summary?.gpus.total;
  const nodeTotal = summary?.nodes.total;
  return <div className="grid">
    {[['GPU', loading ? '—' : (gpuTotal == null ? '—' : String(gpuTotal)), inventoryError ? tx('资产 API 不可用', 'Inventory API unavailable') : tx(`${nodeTotal ?? '—'} 节点 · ${summary?.gpus.known_uuid ?? '—'} UUID`, `${nodeTotal ?? '—'} nodes · ${summary?.gpus.known_uuid ?? '—'} UUIDs`), Cpu], [tx('未恢复事件', 'Open events'), loading ? '—' : String(faultSummary?.open ?? 0), `${faultSummary?.open_by_severity.critical || 0} CRITICAL`, Bell], [tx('涉及主机', 'Affected hosts'), loading ? '—' : String(hosts), tx('未恢复事件范围', 'Open event scope'), Server], [tx('已恢复事件', 'Recovered events'), loading ? '—' : String(faultSummary?.by_state.recovered || 0), `${faultSummary?.total || 0} TOTAL EPISODES`, CheckCircle2]].map(([label, value, note, Icon]) => { const I = Icon as typeof Cpu; return <Card className="metric" key={String(label)}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></Card>; })}
    <Card className="span-8 fleet-card"><CardHead code="FLEET" title={tx('GPU 型号', 'GPU Models')} action={<Badge value={summary?.latest_sync ? tx('已同步', 'SYNCED') : tx('等待同步', 'WAITING')} kind={summary?.latest_sync ? 'healthy' : 'warning'} />} /><div className="fleet-list">{models.map(x => <div key={x.model}><span><b>{x.short}</b><small>{x.model}</small></span><div><i style={{ width: `${gpuTotal ? x.count / gpuTotal * 100 : 0}%` }} /></div><strong>{x.count}</strong></div>)}{models.length === 0 && <Empty tx={tx} title={tx('无资产数据', 'No inventory data')} />}</div></Card>
    <Card className="span-4"><CardHead code="STATUS" title={tx('交付状态', 'Delivery')} /><div className="delivery">{[[tx('数据治理', 'Data governance'), 'P0', tx('基线完成', 'BASELINE')], [tx('健康评分', 'Health scoring'), 'P1', tx('基线完成', 'BASELINE')], [tx('故障闭环', 'Incident workflow'), 'P2', tx('进行中', 'ACTIVE')], [tx('性能验证', 'Performance validation'), 'P2.5', tx('规划', 'PLANNED')]].map(x => <div key={x[1]}><code>{x[1]}</code><b>{x[0]}</b><Badge value={x[2]} kind={x[1] === 'P2' ? 'healthy' : 'neutral'} /></div>)}</div></Card>
    <Card className="span-8"><CardHead code="LIVE" title={tx('未恢复硬件事件', 'Open Hardware Events')} action={<button className="link" onClick={() => navigate('incidents')}>{tx('全部', 'View all')}<ChevronRight size={13} /></button>} /><FaultEventList tx={tx} items={faults.slice(0, 6)} /></Card>
    <Card className="span-4"><CardHead code="SCOPE" title={tx('检测范围', 'Detection Scope')} /><div className="scope-list">{[[ShieldAlert, tx('硬故障', 'Hard failure'), 'XID · DBE · DROP'], [Gauge, tx('性能衰减', 'Degradation'), 'COMPUTE · MEMORY · LINK'], [Activity, tx('异常', 'Anomaly'), 'PEER · HISTORY · DRIFT'], [BrainCircuit, tx('预测', 'Prediction'), '1H · 6H · 24H']].map(([Icon, title, code]) => { const I = Icon as typeof Cpu; return <div key={String(code)}><I size={16} /><span><b>{String(title)}</b><small>{String(code)}</small></span></div>; })}</div></Card>
  </div>;
}

function Issues({ tx, summary, rows, meta, page, loading, category, setCategory, status, setStatus, query, setQuery, previousPage, nextPage, select, lang }: { tx: Tx; summary: IssueSummary | null; rows: PlatformIssue[]; meta: CursorMeta; page: number; loading: boolean; category: string; setCategory: (value: string) => void; status: string; setStatus: (value: string) => void; query: string; setQuery: (value: string) => void; previousPage: () => void; nextPage: () => void; select: (id: number) => void; lang: string }) {
  const categories = [
    ['availability', tx('节点可用性', 'Node Availability'), Server],
    ['inventory', tx('资产与身份', 'Inventory & Identity'), Cpu],
    ['data_quality', tx('数据质量', 'Data Quality'), Activity],
    ['access', tx('节点访问', 'Node Access'), ShieldCheck],
  ] as const;
  const categoryName = (value: string) => categories.find(item => item[0] === value)?.[1] || value;
  const start = meta.total > 0 ? (page - 1) * meta.limit + 1 : 0;
  const end = start > 0 ? start + rows.length - 1 : 0;
  return <div className="grid issue-center">
    {[
      [tx('发现问题', 'Discovered'), summary?.discovered ?? '—', tx('历史累计', 'ALL TIME'), ClipboardList, ''],
      [tx('已解决', 'Resolved'), summary?.resolved ?? '—', tx('自动恢复或人工解决', 'AUTO OR MANUAL'), CheckCircle2, 'resolved'],
      [tx('遗留问题', 'Remaining'), summary?.remaining ?? '—', tx('待处理与处理中', 'OPEN + IN PROGRESS'), AlertTriangle, 'remaining'],
      [tx('当前仍被检测', 'Actively Detected'), summary?.active_detection ?? '—', tx('自动检测源仍异常', 'SOURCE STILL ACTIVE'), ShieldAlert, ''],
    ].map(([label, value, note, Icon, statusFilter]) => { const I = Icon as typeof ClipboardList; return <button className="issue-stat" key={String(label)} onClick={() => { setStatus(String(statusFilter)); setCategory(''); }}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></button>; })}
    <Card className="span-12"><CardHead code="CATEGORIES" title={tx('问题分类', 'Issue Categories')} action={<div className="table-actions"><Badge value={`${summary?.discovered || 0} TOTAL`} kind="info" /><Badge value={`${summary?.training_eligible || 0} TRAINING`} kind={(summary?.training_eligible || 0) > 0 ? 'healthy' : 'neutral'} /><a className="link training-export" href="/api/v1/issues/training-data?download=1" download><Download size={13} />{tx('导出训练数据', 'Export training data')}</a></div>} /><div className="issue-categories"><button className={!category ? 'active' : ''} onClick={() => setCategory('')}><span className="issue-category-icon"><Layers3 size={21} /></span><b>{tx('全部问题', 'All Issues')}</b><strong>{summary?.discovered || 0}</strong><small>all</small></button>{categories.map(([key, label, Icon]) => <button key={key} className={category === key ? 'active' : ''} onClick={() => setCategory(key)}><span className="issue-category-icon"><Icon size={21} /></span><b>{label}</b><strong>{summary?.by_category[key] || 0}</strong><small>{key}</small></button>)}</div></Card>
    <Card className="span-12"><div className="toolbar issue-toolbar"><label><Search size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder={tx('节点 / GPU / 类型 / 描述', 'Node / GPU / type / description')} /></label><label className="select"><Filter size={15} /><select value={status} onChange={event => setStatus(event.target.value)}><option value="">{tx('全部状态', 'All status')}</option><option value="remaining">{tx('遗留问题', 'REMAINING')}</option><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><span>{loading ? tx('同步中', 'Syncing') : `${start}–${end} / ${meta.total}`}</span></div><div className="table-wrap"><table className="issue-table"><thead><tr><th>{tx('分类 / 类型', 'Category / Type')}</th><th>{tx('问题', 'Issue')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('级别', 'Severity')}</th><th>{tx('处理状态', 'Workflow')}</th><th>{tx('检测状态', 'Detection')}</th><th>{tx('最近发现', 'Last Seen')}</th><th /></tr></thead><tbody>{rows.map(issue => <tr key={issue.id}><td><b>{categoryName(issue.category)}</b><small><code>{issue.issue_type}</code></small></td><td><b>{issue.title}</b><small>{issue.description || '—'}</small></td><td><b>{issue.node_ip || issue.entity_key}</b><small><code>{issue.gpu_uuid || issue.entity_type}</code></small></td><td><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /></td><td><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /></td><td><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /></td><td>{time(issue.last_detected_at, lang)}</td><td><button className="link" onClick={() => select(issue.id)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('没有匹配的问题', 'No matching issues')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${page} 页`, `Page ${page}`)}</span><div><button onClick={previousPage} disabled={page <= 1}><ChevronLeft size={15} /></button><button onClick={nextPage} disabled={!meta.has_more}><ChevronRight size={15} /></button></div></div></Card>
  </div>;
}

function Gpus({ tx, view, assets, models, loading, inventoryError, healthScores, healthSummary, lang }: { tx: Tx; view: string; assets: GPUAsset[]; models: { model: string; short: string; count: number }[]; loading: boolean; inventoryError: boolean; healthScores: GPUHealthScore[]; healthSummary: HealthSummary | null; lang: string }) {
  const dims = [
    { code: 'S', name: tx('稳定性', 'Stability'), detail: 'XID / Reset / Drop', icon: ShieldAlert, score: 'stability_score' },
    { code: 'M', name: tx('显存', 'Memory'), detail: 'ECC / SRAM / Row Remap', icon: MemoryStick, score: 'memory_score' },
    { code: 'T', name: tx('散热', 'Thermal'), detail: 'Temperature / Cooling', icon: Thermometer, score: 'thermal_score' },
    { code: 'P', name: tx('供电', 'Power'), detail: 'Power / Clock / Throttle', icon: Zap, score: 'power_score' },
    { code: 'I', name: tx('互联', 'Interconnect'), detail: 'PCIe / NVLink / NVSwitch', icon: Network, score: 'interconnect_score' },
    { code: 'F', name: tx('性能', 'Performance'), detail: 'Compute / Bandwidth / Straggler', icon: Gauge, score: 'performance_score' },
  ] as const;
  const [nodePage, setNodePage] = useState(0);
  const [expandedNodes, setExpandedNodes] = useState<Set<string>>(() => new Set());
  const nodePageSize = 20;
  const groupedNodes = [...assets.reduce((groups, asset) => {
    const cards = groups.get(asset.node_ip) || [];
    cards.push(asset);
    groups.set(asset.node_ip, cards);
    return groups;
  }, new Map<string, GPUAsset[]>()).entries()].map(([nodeIP, cards]) => {
    const sortedCards = [...cards].sort((a, b) => Number(a.state === 'active') - Number(b.state === 'active') || a.gpu_index - b.gpu_index);
    const active = cards.filter(card => card.state === 'active').length;
    const modelsOnNode = [...new Set(cards.map(card => card.model_name || card.model || 'UNKNOWN'))];
    const lastSynced = cards.reduce((latest, card) => !latest || card.last_synced_at > latest ? card.last_synced_at : latest, '');
    return { nodeIP, cards: sortedCards, active, abnormal: cards.length - active, models: modelsOnNode, lastSynced };
  }).sort((a, b) => b.abnormal - a.abnormal || compareIP(a.nodeIP, b.nodeIP));
  const nodePageCount = Math.max(1, Math.ceil(groupedNodes.length / nodePageSize));
  const safeNodePage = Math.min(nodePage, nodePageCount - 1);
  const visibleNodes = groupedNodes.slice(safeNodePage * nodePageSize, (safeNodePage + 1) * nodePageSize);
  const abnormalCards = assets.filter(asset => asset.state !== 'active').length;
  const toggleNode = (nodeIP: string) => setExpandedNodes(current => { const next = new Set(current); if (next.has(nodeIP)) next.delete(nodeIP); else next.add(nodeIP); return next; });
  if (view === 'health') {
    const risky = healthScores.filter(x => x.score == null || x.level !== 'healthy' || x.data_confidence !== 'A' || (x.fallback_metric_count || 0) > 0).sort((a, b) => {
      if (a.score == null && b.score == null) return compareIP(a.node_ip, b.node_ip) || a.gpu_index - b.gpu_index;
      if (a.score == null) return 1;
      if (b.score == null) return -1;
      return a.score - b.score || compareIP(a.node_ip, b.node_ip) || a.gpu_index - b.gpu_index;
    }).slice(0, 100);
    const riskCount = (healthSummary?.by_level.attention || 0) + (healthSummary?.by_level.warning || 0) + (healthSummary?.by_level.critical || 0);
    const scoredCoverage = healthSummary ? `${healthSummary.scored} / ${healthSummary.total} GPU` : '—';
    return <div className="grid">
      <section className="health-kpi-panel">
        <header><div><code>HEALTH OUTCOME</code><h2>{tx('核心健康', 'Core Health')}</h2></div><small>{tx('规则评分结果；UNKNOWN 不参与平均分', 'Rule outcomes; UNKNOWN is excluded from the average')}</small></header>
        <div className="health-kpi-grid">
          {[[tx('已评分平均分', 'Scored average'), healthSummary ? healthSummary.average_score.toFixed(1) : '—', `${scoredCoverage} · ${tx('未知已排除', 'UNKNOWN EXCLUDED')}`, CircleGauge], [tx('风险卡', 'At-risk GPUs'), String(riskCount), 'ATTENTION + WARNING + CRITICAL', ShieldAlert], [tx('数据未知', 'Unknown'), String(healthSummary?.unknown ?? '—'), tx('指标不足，分数为空', 'INSUFFICIENT DATA · SCORE NULL'), AlertTriangle], [tx('规则命中', 'Rule hits'), String(healthSummary?.latest_run?.rule_hit_count ?? '—'), healthSummary?.latest_run?.rule_version || 'WAITING', Activity]].map(([label, value, note, Icon]) => { const I = Icon as typeof Cpu; return <Card className="metric" key={String(label)}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></Card>; })}
        </div>
      </section>
      <Card className="span-12"><CardHead code="RISK QUEUE" title={tx('GPU 健康风险', 'GPU Health Risks')} action={<Badge value={`${risky.length} / ${healthScores.length}`} kind={risky.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="health-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('分数', 'Score')}</th><th>{tx('级别', 'Level')}</th><th>{tx('置信度', 'Confidence')}</th><th>{tx('健康领域分数', 'Health Domain Scores')}</th><th>{tx('详情', 'Details')}</th><th>{tx('计算时间', 'Evaluated')}</th></tr></thead><tbody>{risky.map(score => <tr key={score.gpu_asset_id}><td><b>{score.node_ip} · GPU {score.gpu_index}</b><small><code>{score.gpu_uuid}</code></small></td><td>{score.model_name}</td><td className="score-cell"><strong className="score-value">{score.score ?? '—'}</strong></td><td><Badge value={score.level.toUpperCase()} kind={tone(score.level)} /></td><td><Badge value={score.data_confidence} kind={score.data_confidence === 'A' ? 'healthy' : score.data_confidence === 'B' ? 'info' : score.data_confidence === 'C' ? 'warning' : 'danger'} /></td><td><div className="domain-scores">{dims.map(domain => <span key={domain.code}><b>{domain.code} {domain.name}</b><strong>{score[domain.score]}</strong></span>)}</div></td><td><small>{(score.evidence || []).join(' · ')}</small><small>{tx('取值来源', 'Selected sources')}: {(score.sources_available || []).join(' + ') || '—'} · fallback {score.fallback_metric_count || 0}</small></td><td>{time(score.evaluated_at, lang)}</td></tr>)}</tbody></table>{!loading && healthScores.length === 0 && <Empty tx={tx} title={tx('等待首次健康评分', 'Waiting for first health evaluation')} />}</div></Card>
      <Card className="span-12"><CardHead code="DOMAINS" title={tx('GPU 健康领域', 'GPU Health Domains')} /><div className="domain-grid">{dims.map(domain => { const Icon = domain.icon; return <div key={domain.code}><Icon size={17} /><b>{domain.code} · {domain.name}</b><span>{domain.detail}</span></div>; })}</div></Card>
    </div>;
  }
  return <div className="grid"><Card className="span-12"><CardHead code="INVENTORY" title={tx('资产基线', 'Inventory Baseline')} /><div className="inventory inventory-models">{models.map(x => <div key={x.model}><i /><span><b>{x.short}</b><small>{x.model}</small></span><strong>{x.count}</strong></div>)}{models.length === 0 && <Empty tx={tx} title={tx('等待资产同步', 'Waiting for inventory sync')} />}</div></Card><Card className="span-12"><CardHead code="GPU NODES" title={tx('GPU 资产清单', 'GPU Inventory')} action={<div className="table-actions"><Badge value={inventoryError ? tx('接口异常', 'API ERROR') : tx(`总卡数 ${assets.length}`, `${assets.length} TOTAL GPUS`)} kind={inventoryError ? 'warning' : 'info'} /><Badge value={tx(`异常卡数 ${abnormalCards}`, `${abnormalCards} NON-ACTIVE`)} kind={abnormalCards ? 'warning' : 'healthy'} /><button disabled={safeNodePage === 0} onClick={() => setNodePage(page => page - 1)} aria-label={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><span>{safeNodePage + 1} / {nodePageCount}</span><button disabled={safeNodePage + 1 >= nodePageCount} onClick={() => setNodePage(page => page + 1)} aria-label={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div>} /><div className="table-wrap"><table className="node-inventory-table"><thead><tr><th>{tx('节点 IP', 'Node IP')}</th><th>{tx('GPU 型号', 'GPU Models')}</th><th>{tx('卡状态', 'GPU State')}</th><th>{tx('主机状态', 'Node State')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{visibleNodes.map(node => <Fragment key={node.nodeIP}><tr className={node.abnormal ? 'node-abnormal' : ''}><td><button className="node-expand" onClick={() => toggleNode(node.nodeIP)}><ChevronRight className={expandedNodes.has(node.nodeIP) ? 'expanded' : ''} size={15} /><b>{node.nodeIP}</b></button><small>{node.cards.length} GPU</small></td><td>{node.models.map(model => <small key={model}>{model}</small>)}</td><td><b>{node.active} ACTIVE</b>{node.abnormal > 0 && <small className="state-warning">{node.abnormal} NON-ACTIVE</small>}</td><td><Badge value={node.abnormal ? `${node.active} ACTIVE · ${node.abnormal} NON-ACTIVE` : `${node.active} ACTIVE`} kind={node.abnormal ? 'warning' : 'healthy'} /></td><td>{time(node.lastSynced, lang)}</td></tr>{expandedNodes.has(node.nodeIP) && <tr className="gpu-detail-row"><td colSpan={5}><div className="table-wrap"><table className="gpu-detail-table"><thead><tr><th>GPU</th><th>GPU UUID</th><th>{tx('型号', 'Model')}</th><th>PCI BUS ID</th><th>{tx('状态', 'State')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{node.cards.map(card => <tr key={card.asset_key}><td><b>GPU {card.gpu_index}</b><small>{card.device || '—'}</small></td><td><code>{card.gpu_uuid || 'UUID UNKNOWN'}</code></td><td>{card.model_name || card.model || 'UNKNOWN'}</td><td><code>{card.pci_bus_id || '—'}</code></td><td><Badge value={card.state.toUpperCase()} kind={card.state === 'active' ? 'healthy' : 'warning'} /></td><td>{time(card.last_synced_at, lang)}</td></tr>)}</tbody></table></div></td></tr>}</Fragment>)}</tbody></table>{!loading && assets.length === 0 && <Empty tx={tx} title={inventoryError ? tx('资产 API 不可用', 'Inventory API unavailable') : tx('等待首次同步', 'Waiting for first sync')} />}</div></Card></div>;
}

function Incidents({ tx, view, rows, ingestionMeta, ingestionPage, ingestionLoading, previousIngestionPage, nextIngestionPage, faultRows, faultMeta, faultPage, faultLoading, previousFaultPage, nextFaultPage, query, setQuery, level, setLevel, failures, select, selectIssue, selectReport, lang }: { tx: Tx; view: string; rows: Ingestion[]; ingestionMeta: IngestionMeta; ingestionPage: number; ingestionLoading: boolean; previousIngestionPage: () => void; nextIngestionPage: () => void; faultRows: FaultEvent[]; faultMeta: CursorMeta; faultPage: number; faultLoading: boolean; previousFaultPage: () => void; nextFaultPage: () => void; query: string; setQuery: (v: string) => void; level: string; setLevel: (v: string) => void; failures: Failure[]; select: (id: number) => void; selectIssue: (id: number) => void; selectReport: (id: number) => void; lang: string }) {
  if (view === 'workflow') return <div className="grid"><Card className="span-12"><CardHead code="WORKFLOW" title={tx('处理状态', 'Workflow State')} action={failures.length ? <Badge value={`${failures.length} INGEST FAILED`} kind="warning" /> : undefined} /><div className="workflow">{[tx('发现', 'DETECTED'), tx('分诊', 'TRIAGED'), tx('待维护', 'PENDING'), tx('已隔离', 'ISOLATED'), tx('诊断', 'DIAGNOSIS'), tx('修复', 'REPAIRED'), tx('复测', 'VALIDATED'), tx('关闭', 'CLOSED')].map((x, i) => <div key={x}><small>{String(i + 1).padStart(2, '0')}</small><b>{x}</b>{i < 7 && <ChevronRight size={13} />}</div>)}</div></Card></div>;
  if (view === 'ingestion') {
    const status = ingestionMeta.stream_status.toUpperCase();
    const statusTone = status === 'LIVE' ? 'healthy' : status === 'SNAPSHOT' ? 'info' : status === 'ERROR' ? 'danger' : status === 'STALE' ? 'warning' : 'neutral';
    const start = ingestionMeta.total > 0 ? (ingestionPage - 1) * ingestionMeta.limit + 1 : 0;
    const end = start > 0 ? start + rows.length - 1 : 0;
    return <div className="grid">
      <Card className="span-12 ingestion-stream"><div><CardHead code="INGESTION STREAM" title={tx('告警接收链路', 'Alert Ingestion Stream')} /><p>{tx('原始监控告警接收与持久化审计；不等同于 Atlas 硬件事件。', 'Raw monitoring alert ingestion and persistence audit; separate from Atlas hardware events.')}</p></div><div className="ingestion-stream-metrics"><span><small>{tx('状态', 'Status')}</small><Badge value={ingestionLoading ? 'SYNCING' : status} kind={ingestionLoading ? 'info' : statusTone} /></span><span><small>{tx('数据模式', 'Data mode')}</small><b>{ingestionMeta.source_mode.toUpperCase()}</b></span><span><small>{tx('最新接收', 'Latest received')}</small><b>{ingestionMeta.latest_received_at ? time(ingestionMeta.latest_received_at, lang) : '—'}</b></span><span><small>{tx('近 5 分钟', 'Last 5m')}</small><b>{ingestionMeta.received_5m}</b></span><span><small>{tx('近 1 小时', 'Last 1h')}</small><b>{ingestionMeta.received_1h}</b></span><span><small>{tx('全部记录', 'All records')}</small><b>{ingestionMeta.all_total}</b></span></div></Card>
      <Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('主机 / UUID / XID / 消息', 'Host / UUID / XID / message')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'error', 'warning', 'info'].map(x => <option key={x}>{x}</option>)}</select></label><span>{start}–{end} / {ingestionMeta.total}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('事件', 'Incident')}</th><th>{tx('主机 / GPU', 'Host / GPU')}</th><th>{tx('来源', 'Source')}</th><th>{tx('处理', 'State')}</th><th>{tx('告警时间', 'Alert Time')}</th><th /></tr></thead><tbody>{rows.map(x => <tr key={x.id}><td><Badge value={x.level || 'unknown'} kind={tone(x.level)} /></td><td><b>{x.message}</b><small>#{x.id} · {x.labels?.err_msg || x.event_id}</small></td><td>{x.host || '—'}<small><code>{x.labels?.UUID || x.labels?.uuid || x.labels?.pci_bus_id || 'UNMAPPED'}</code></small></td><td>{x.source}</td><td><Badge value={x.process_status} kind={x.process_status === 'success' ? 'healthy' : 'warning'} /></td><td>{time(x.event_timestamp || x.created_at, lang)}<small>{x.event_timestamp ? tx('源事件时间', 'source event') : tx('接收时间', 'received')}</small></td><td><button className="link" onClick={() => select(x.id)}>{tx('详情', 'Details')}<Eye size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('无匹配接收记录', 'No matching ingestion records')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${ingestionPage} 页`, `Page ${ingestionPage}`)}</span><div><button onClick={previousIngestionPage} disabled={ingestionPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextIngestionPage} disabled={!ingestionMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card>
    </div>;
  }
  const faultStart = faultMeta.total > 0 ? (faultPage - 1) * faultMeta.limit + 1 : 0;
  const faultEnd = faultStart > 0 ? faultStart + faultRows.length - 1 : 0;
  return <div className="grid"><Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('节点 / UUID / 规则 / 详情', 'Node / UUID / rule / details')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'warning', 'attention'].map(x => <option key={x}>{x}</option>)}</select></label><span>{faultLoading ? tx('加载中', 'Loading') : `${faultStart}–${faultEnd} / ${faultMeta.total}`}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('规则 / 详情', 'Rule / Details')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('维度', 'Domain')}</th><th>{tx('告警状态', 'Alert')}</th><th>{tx('处置状态', 'Workflow')}</th><th>{tx('次数', 'Count')}</th><th>{tx('首次 / 最近', 'First / Last')}</th><th /></tr></thead><tbody>{faultRows.map(x => <tr key={x.id}><td><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /></td><td><b>{x.rule_code}</b><small>{x.evidence} · {tx('阈值', 'threshold')} {x.threshold}</small></td><td><b>{x.node_ip} · GPU {x.gpu_index}</b><small><code>{x.gpu_uuid}</code></small></td><td><code>{x.domain.toUpperCase()}</code></td><td><Badge value={x.state.toUpperCase()} kind={tone(x.state)} /></td><td><Badge value={(x.workflow_status || 'syncing').toUpperCase()} kind={x.workflow_status ? issueTone(x.workflow_status) : 'neutral'} /></td><td>{x.occurrence_count}</td><td>{time(x.first_observed_at, lang)}<small>{time(x.last_observed_at, lang)}</small></td><td><div className="event-actions"><button className="link" onClick={() => selectReport(x.id)}>{tx('分析报告', 'Report')}<BookOpen size={13} /></button>{x.issue_id ? <button className="link" onClick={() => selectIssue(x.issue_id!)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button> : <small>{tx('台账同步中', 'Syncing')}</small>}</div></td></tr>)}</tbody></table>{faultRows.length === 0 && <Empty tx={tx} title={tx('无匹配硬件告警', 'No matching hardware alerts')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${faultPage} 页`, `Page ${faultPage}`)}</span><div><button onClick={previousFaultPage} disabled={faultPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextFaultPage} disabled={!faultMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card></div>;
}

function Validations({ tx, view, summary, candidates, lang }: { tx: Tx; view: string; summary: DegradationSummary | null; candidates: DegradationCandidate[]; lang: string }) {
  if (view === 'records') return <div className="grid">
    <Card className="span-12 safety"><ShieldAlert size={21} /><div><CardHead code="SAFETY" title={tx('主动测试前置条件', 'Active Test Gates')} /><p>{tx('人工确认维护窗口 · 无 GPU 计算进程 · 操作确认 · 超时与温控保护', 'Maintenance confirmed · no GPU compute process · operator confirmation · timeout and thermal guard')}</p></div><Badge value="NO AUTO STRESS" kind="warning" /></Card>
    <Card className="span-12"><CardHead code="QUEUE" title={tx('验证记录', 'Validation Records')} /><Empty tx={tx} title={tx('主动验证接口尚未启用', 'Active validation API is not enabled')} /></Card>
  </div>;
  const scope = (value: string) => value === 'same_model_high_load_7d' ? tx('同型号高负载 7 天', 'Same-model high-load 7d') : value === 'same_node_model' ? tx('同节点同型号', 'Same node/model') : tx('同型号集群', 'Same-model fleet');
  return <div className="grid">
    <Card className="span-12 degradation-guard"><div><Eye size={21} /><div><CardHead code="SHADOW MODE" title={tx('被动衰减候选', 'Passive Degradation Candidates')} /><p>{tx('只读观察：不扣减健康分、不输出故障概率、不自动隔离或压测。候选必须先确认负载可比性，再进入维护窗口复测。', 'Read-only observation: no health deduction, failure probability, automatic isolation or stress. Confirm workload comparability before maintenance validation.')}</p></div></div><Badge value={(summary?.mode || 'shadow').toUpperCase()} kind="info" /></Card>
    <Card className="span-3 degradation-stat"><small>{tx('已评估', 'EVALUATED')}</small><strong>{summary?.evaluated_gpus ?? '—'}</strong><span>{tx('当前健康快照', 'current health snapshots')}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('高负载可评估', 'LOAD ELIGIBLE')}</small><strong>{summary?.eligible_gpus ?? '—'}</strong><span>{tx(`利用率 ≥ ${summary?.minimum_utilization ?? 80}%`, `utilization ≥ ${summary?.minimum_utilization ?? 80}%`)}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('基线就绪', 'BASELINE READY')}</small><strong>{summary?.baseline_ready_gpus ?? '—'}</strong><span>{tx(`历史基线 ${summary?.historical_baseline_gpus ?? 0}`, `${summary?.historical_baseline_gpus ?? 0} historical`)}</span></Card>
    <Card className="span-3 degradation-stat candidate"><small>{tx('衰减候选', 'CANDIDATES')}</small><strong>{summary?.candidate_gpus ?? '—'}</strong><span>{tx(`性能比 < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`, `performance ratio < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`)}</span></Card>
    <Card className="span-12 degradation-table-card"><CardHead code={summary?.version || 'degradation-v0.2.0'} title={tx('候选明细', 'Candidate Details')} action={<span className="table-observed">{tx('最近观测', 'LATEST OBSERVED')} {time(summary?.latest_observed_at, lang)}</span>} />
      {candidates.length > 0 ? <div className="table-wrap"><table><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('负载', 'Load')}</th><th>{tx('时钟观测 / 基线', 'Clock observed / baseline')}</th><th>{tx('性能比', 'Ratio')}</th><th>{tx('基线', 'Baseline')}</th><th>{tx('置信度', 'Confidence')}</th><th>{tx('建议', 'Recommendation')}</th></tr></thead><tbody>{candidates.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip}</b><small>GPU {item.gpu_index} · {item.gpu_uuid || 'UUID UNKNOWN'}</small></td><td>{item.model_name || 'UNKNOWN'}</td><td>{item.gpu_utilization.toFixed(1)}%</td><td><b>{item.observed_value.toFixed(0)} MHz</b><small>{item.baseline_value.toFixed(0)} MHz</small></td><td><Badge value={`${(item.performance_ratio * 100).toFixed(1)}%`} kind="warning" /></td><td><b>{scope(item.baseline_scope)}</b><small>{item.peer_count} {tx('个对照 GPU', 'peer GPUs')}</small></td><td><Badge value={item.data_confidence} kind={item.data_confidence === 'A' ? 'healthy' : 'info'} /></td><td><span className="recommendation">{tx('确认工作负载可比性后，在维护窗口执行 DCGM / SuperBench 复测。', 'Confirm workload comparability, then run DCGM / SuperBench in a maintenance window.')}</span><small>{tx('源时间', 'source time')} {time(item.observed_at, lang)}</small></td></tr>)}</tbody></table></div> : <Empty tx={tx} title={tx('当前没有被动衰减候选', 'No passive degradation candidates')} />}
    </Card>
    <Card className="span-12 degradation-method"><CardHead code="METHOD" title={tx('当前判定口径', 'Current Decision Contract')} /><div className="stages"><div><b>01 / ELIGIBILITY</b><h3>{tx('高负载新鲜观测', 'Fresh High-load Observation')}</h3><p>gpu_util_avg_15m ≥ {summary?.minimum_utilization ?? 80}% · confidence A/B · freshness ≤ {Math.round((summary?.freshness_sla_seconds ?? 3600) / 60)}m</p><Badge value="NON-INTRUSIVE" kind="healthy" /></div><ChevronRight /><div><b>02 / BASELINE</b><h3>{tx('稳健历史与同类基线', 'Robust Historical & Peer Baseline')}</h3><p>{tx('成熟的同型号高负载 7 天基线优先 · 同节点/集群实时中位数兜底', 'mature same-model high-load 7d first · live node/fleet median fallback')}</p><Badge value="FEATURE BASELINE v1" kind="info" /></div><ChevronRight /><div><b>03 / VALIDATE</b><h3>{tx('人工维护复测', 'Human-gated Validation')}</h3><p>DCGM · SuperBench · workload context</p><Badge value="NOT AUTOMATED" kind="warning" /></div></div></Card>
  </div>;
}
function Quality({ tx, view, targets, summary, issueSummary, inventoryError, syncRuns, assetChanges, telemetry, telemetrySummary, nodeAccess, reloadNodeAccess, openIssues, lang }: { tx: Tx; view: string; targets: CollectorTarget[]; summary: FleetSummary | null; issueSummary: IssueSummary | null; inventoryError: boolean; syncRuns: SyncRun[]; assetChanges: AssetChange[]; telemetry: TelemetryQualityItem[]; telemetrySummary: TelemetryQualitySummary | null; nodeAccess: NodeAccessOverview | null; reloadNodeAccess: () => void; openIssues: () => void; lang: string }) {
  const [credentialDraft, setCredentialDraft] = useState({ profile_id: '', priority: 10, username: '', password: '', enabled: true });
  const [credentialAdminToken, setCredentialAdminToken] = useState('');
  const [credentialSaving, setCredentialSaving] = useState(false);
  const [credentialMessage, setCredentialMessage] = useState('');
  const [credentialError, setCredentialError] = useState('');
  const [checkNodeIP, setCheckNodeIP] = useState('');
  const [connectivityChecks, setConnectivityChecks] = useState<NodeAccessCheck[]>([]);
  const [connectivityChecking, setConnectivityChecking] = useState(false);
  const [connectivityError, setConnectivityError] = useState('');
  const credentialTransportReady = window.location.protocol === 'https:' || ['localhost', '127.0.0.1', '::1'].includes(window.location.hostname) || Boolean(nodeAccess?.insecure_http_allowed);
  const loadConnectivityChecks = async () => {
    try {
      const response = await fetch('/api/v1/node-access/checks');
      if (!response.ok) return;
      const payload = await response.json();
      setConnectivityChecks(payload.data || []);
    } catch { /* overview remains usable when the check ledger is unavailable */ }
  };
  useEffect(() => { if (view === 'node-access') void loadConnectivityChecks(); }, [view]);
  const runConnectivityCheck = async () => {
    setConnectivityChecking(true); setConnectivityError('');
    try {
      const response = await fetch('/api/v1/node-access/checks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': credentialAdminToken },
        body: JSON.stringify({ node_ip: checkNodeIP }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCheckNodeIP('');
      await loadConnectivityChecks();
      reloadNodeAccess();
    } catch (reason) {
      setConnectivityError(reason instanceof Error ? reason.message : tx('检查失败', 'Check failed'));
    } finally { setConnectivityChecking(false); }
  };
  const saveCredential = async () => {
    setCredentialSaving(true); setCredentialMessage(''); setCredentialError('');
    try {
      const response = await fetch('/api/v1/node-access/credentials', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': credentialAdminToken },
        body: JSON.stringify(credentialDraft),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCredentialDraft({ profile_id: '', priority: credentialDraft.priority + 10, username: '', password: '', enabled: true });
      setCredentialMessage(tx('凭据已加密保存；页面不会再次显示账号或密码', 'Credential encrypted and saved; the account and password will not be shown again'));
      reloadNodeAccess();
    } catch (reason) {
      setCredentialError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setCredentialSaving(false); }
  };
  const deleteCredential = async (profileID: string) => {
    if (!window.confirm(tx(`确认删除凭据配置 ${profileID}？删除后无法恢复。`, `Delete credential profile ${profileID}? This cannot be undone.`))) return;
    setCredentialError(''); setCredentialMessage('');
    try {
      const response = await fetch(`/api/v1/node-access/credentials/${encodeURIComponent(profileID)}`, { method: 'DELETE', headers: { 'X-Atlas-Admin-Token': credentialAdminToken } });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCredentialMessage(tx('凭据配置已删除', 'Credential profile deleted'));
      reloadNodeAccess();
    } catch (reason) { setCredentialError(reason instanceof Error ? reason.message : tx('删除失败', 'Delete failed')); }
  };
  const definitions = [['dcgm_exporter', 'DCGM Exporter', 'GPU Telemetry'], ['gpu_exporter', 'GPU Exporter', 'NVML Extension'], ['node_exporter', 'Node Exporter', 'Host OS'], ['ipmi_exporter', 'IPMI Exporter', 'BMC / Hardware']] as const;
  const qualityLedger = <Card className="span-12 quality-ledger-card"><CardHead code="DATA QUALITY LEDGER" title={tx('数据质量问题统计', 'Data Quality Issue Statistics')} action={<div className="table-actions"><Badge value={`${issueSummary?.remaining_by_category?.data_quality || 0} REMAINING`} kind={(issueSummary?.remaining_by_category?.data_quality || 0) > 0 ? 'warning' : 'healthy'} /><button className="link quality-ledger-link" onClick={openIssues}>{tx('查看问题列表', 'View issues')}<ChevronRight size={13} /></button></div>} /><div className="issue-categories quality-ledger"><div><b>{tx('历史发现', 'Discovered')}</b><strong>{issueSummary?.by_category?.data_quality || 0}</strong><small>DATA QUALITY</small></div><div><b>{tx('已解决', 'Resolved')}</b><strong>{issueSummary?.resolved_by_category?.data_quality || 0}</strong><small>AUTO + MANUAL</small></div><div><b>{tx('遗留问题', 'Remaining')}</b><strong>{issueSummary?.remaining_by_category?.data_quality || 0}</strong><small>OPEN + IN PROGRESS</small></div><div><b>{tx('当前检测', 'Actively Detected')}</b><strong>{issueSummary?.active_by_category?.data_quality || 0}</strong><small>SOURCE ACTIVE</small></div></div></Card>;
  if (view === 'issues') return <div className="grid">
    <Card className="span-12 capability-note"><ClipboardList size={19} /><div><CardHead code="QUALITY WORKFLOW" title={tx('数据质量问题台账', 'Data Quality Issue Ledger')} /><p>{tx('集中展示数据质量问题的累计发现、已解决、遗留和当前检测数量。查看问题列表会进入数据统计，并自动应用数据质量分类筛选。', 'Review discovered, resolved, remaining and actively detected data-quality findings in one place. View issues opens Data Statistics with the data-quality category applied automatically.')}</p></div><Badge value="CLASSIFIED" kind="info" /></Card>
    {qualityLedger}
  </div>;
  if (view === 'continuity') {
    const problems = telemetry.filter(item => item.status !== 'fresh');
    return <div className="grid">
      <Card className="span-12 capability-note"><Activity size={19} /><div><CardHead code={telemetrySummary?.feature_catalog_version || 'FEATURE CATALOG'} title={tx('GPU 指标连续性', 'GPU Metric Continuity')} /><p>{tx('以现场 15 秒采样周期为基线，联合检查每卡 1 小时样本存在率、最大间隔、UUID 波动，以及 DCGM Target 的五分钟抓取成功率、样本量和耗时变化。该结果只判断遥测可信度，不作为硬件故障或性能异常。', 'Using the observed 15-second cadence, Atlas checks per-GPU one-hour presence, maximum gap and UUID flaps together with five-minute DCGM target success, sample-volume and duration ratios. This evaluates telemetry trustworthiness, not hardware faults or performance anomalies.')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>
      {[[tx('GPU 总数', 'Total GPUs'), telemetrySummary?.total ?? '—', 'LATEST RUN'], [tx('连续正常', 'Fresh'), telemetrySummary?.by_status.fresh ?? 0, '≥95% · AGE≤60s'], [tx('连续性异常', 'Degraded / Stale'), (telemetrySummary?.by_status.degraded || 0) + (telemetrySummary?.by_status.stale || 0), '<95% OR AGE>60s'], [tx('平均存在率', 'Average Presence'), telemetrySummary ? `${telemetrySummary.average_presence_ratio_1h.toFixed(2)}%` : '—', `MAX GAP ${telemetrySummary?.max_metric_gap_seconds_1h?.toFixed(1) || '—'}s · FLAP ${telemetrySummary?.max_uuid_flap_count_1h?.toFixed(0) || '0'}`]].map(([label, value, note]) => <Card className="metric quality compact-quality-metric" key={String(label)}><Activity size={17} /><strong>{String(value)}</strong><b>{String(label)}</b><small>{String(note)}</small></Card>)}
      <Card className="span-12"><CardHead code="CONTINUITY ISSUES" title={tx('异常与未知 GPU', 'Degraded and Unknown GPUs')} action={<div className="table-actions"><Badge value={`SCRAPE ${telemetrySummary?.min_target_scrape_success_ratio_5m?.toFixed(1) || '—'}%`} kind={(telemetrySummary?.min_target_scrape_success_ratio_5m ?? 100) < 95 ? 'warning' : 'healthy'} /><Badge value={`${problems.length} ITEMS`} kind={problems.length ? 'warning' : 'healthy'} /></div>} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>UUID</th><th>{tx('状态', 'Status')}</th><th>{tx('1h 样本 / 存在率', '1h Samples / Presence')}</th><th>{tx('年龄 / 最大间隔', 'Age / Max Gap')}</th><th>{tx('UUID 波动', 'UUID Flaps')}</th><th>{tx('Target 抓取', 'Target Scrape')}</th><th>{tx('观测时间', 'Observed')}</th></tr></thead><tbody>{problems.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip} · GPU {item.gpu_index}</b><small>{item.model_name}</small></td><td><code>{item.gpu_uuid}</code></td><td><Badge value={item.status.toUpperCase()} kind={item.status === 'stale' ? 'danger' : 'warning'} /></td><td>{item.sample_count_1h?.toFixed(0) ?? '—'} / 240<small>{item.presence_ratio_1h != null ? `${item.presence_ratio_1h.toFixed(1)}%` : '—'}</small></td><td>{item.sample_age_seconds != null ? `${item.sample_age_seconds.toFixed(1)}s` : '—'}<small>{tx('最大', 'max')} {item.metric_gap_max_seconds_1h != null ? `${item.metric_gap_max_seconds_1h.toFixed(1)}s` : '—'}</small></td><td>{item.uuid_presence_flap_count_1h?.toFixed(0) ?? '—'}</td><td>{item.target_scrape_success_ratio_5m != null ? `${item.target_scrape_success_ratio_5m.toFixed(1)}%` : '—'}<small>{tx('样本', 'samples')} {item.target_scrape_samples_ratio_5m?.toFixed(1) ?? '—'}% · {tx('耗时比', 'duration')} {item.target_scrape_duration_ratio_5m?.toFixed(1) ?? '—'}%</small></td><td>{time(item.observed_at, lang)}</td></tr>)}</tbody></table>{problems.length === 0 && <Empty tx={tx} title={tx('GPU 指标连续性正常', 'GPU metric continuity is healthy')} />}</div></Card>
    </div>;
  }
  if (view === 'node-access') {
    const local = (value: LocalizedText) => lang.startsWith('zh') ? value.zh : value.en;
    const readOnly = nodeAccess?.commands.filter(command => command.approval_class === 'read_only') || [];
    const approvalRequired = nodeAccess?.commands.filter(command => command.approval_class === 'approval_required') || [];
    const statusKind = nodeAccess?.status === 'connectivity_ready' ? 'healthy' : nodeAccess?.status === 'credentials_missing' || nodeAccess?.status === 'known_hosts_missing' ? 'warning' : 'neutral';
    return <div className="grid node-access-page">
      <Card className="span-12 node-access-guard">
        <div><ShieldCheck size={21} /><div><CardHead code={`${nodeAccess?.skill_id || 'atlas-node-evidence'} / ${nodeAccess?.skill_version || 'v0.3.2'}`} title={tx('节点只读证据 Skill', 'Node Read-only Evidence Skill')} /><p>{tx('低负载只读信息按注册命令和固定资源预算默认采集，无需逐节点制定计划或人工确认；实际受控采集器仍在下一阶段接入。', 'Low-impact read-only evidence uses registered commands and fixed resource budgets by default without per-node planning or confirmation. The controlled collection runner remains the next delivery stage.')}</p></div></div>
        <div className="node-access-badges"><Badge value={(nodeAccess?.status || 'unavailable').toUpperCase()} kind={statusKind} /><Badge value="HANDSHAKE ONLY" kind="info" /><Badge value="NO COMMAND EXECUTED" kind="healthy" /></div>
      </Card>
      {[
        [tx('连接超时', 'Connect timeout'), `${nodeAccess?.budget.connect_timeout_seconds ?? '—'}s`, 'PER ATTEMPT'],
        [tx('命令超时', 'Command timeout'), `${nodeAccess?.budget.command_timeout_seconds ?? '—'}s`, 'PER COMMAND'],
        [tx('输出上限', 'Output limit'), nodeAccess ? `${Math.round(nodeAccess.budget.max_output_bytes / 1024)} KiB` : '—', 'PER COMMAND'],
        [tx('并发节点', 'Concurrent nodes'), nodeAccess?.budget.max_concurrent_nodes ?? '—', `MAX ${nodeAccess?.budget.max_commands_per_node ?? '—'} COMMANDS / NODE`],
      ].map(([label, value, note]) => <Card className="metric quality node-access-stat" key={String(label)}><ShieldCheck size={17} /><strong>{String(value)}</strong><b>{String(label)}</b><small>{String(note)}</small></Card>)}
      <Card className="span-12">
        <CardHead code="FOUNDATIONAL SKILLS" title={tx('基础 Skill 目录', 'Foundational Skill Catalog')} action={<Badge value={`${(nodeAccess?.skills || []).length} SKILLS`} kind="info" />} />
        <p className="node-access-note">{tx('先建立证据采集、故障分析和案例学习三项基础契约。当前不提供维护、重启、节点变更或任务操作 Skill。', 'The first foundation covers evidence collection, fault analysis, and case learning. No maintenance, restart, node-change, or workload-operation Skill is provided.')}</p>
        <div className="node-skill-grid">{(nodeAccess?.skills || []).map(skill => <article key={skill.id}><header><code>{skill.id}</code><Badge value={skill.status.toUpperCase()} kind={skill.status.endsWith('_baseline') ? 'healthy' : 'info'} /></header><b>{local(skill.purpose)}</b><small>{skill.class.toUpperCase()} · {skill.version}</small></article>)}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="CREDENTIAL ROTATION" title={tx('加密凭据字典与尝试顺序', 'Encrypted Credential Dictionary & Attempt Order')} action={<div className="table-actions"><Badge value={nodeAccess?.encryption_ready ? 'AES-256-GCM' : 'ENCRYPTION OFFLINE'} kind={nodeAccess?.encryption_ready ? 'healthy' : 'warning'} /><Badge value={`${nodeAccess?.credential_profiles.length || 0} PROFILES`} kind={nodeAccess?.credential_profiles.some(profile => profile.secret_available) ? 'healthy' : 'neutral'} /></div>} />
        <p className="node-access-note">{tx('账号和密码使用服务端独立主密钥加密保存。页面、列表、API 和日志只展示掩码与状态，保存后不能取回明文。认证拒绝时按优先级尝试下一配置；网络或主机指纹异常立即停止。', 'Accounts and passwords are encrypted using a dedicated server-side master key. Pages, lists, APIs, and logs expose only masked metadata and status; plaintext cannot be retrieved after saving. Authentication rejection advances to the next priority, while network or host-identity failures stop immediately.')}</p>
        <div className="credential-layout">
          <form className="credential-form" onSubmit={event => { event.preventDefault(); void saveCredential(); }}>
            <div className="credential-form-head"><div><b>{tx('新增凭据配置', 'Add credential profile')}</b><small>{nodeAccess?.insecure_http_allowed && window.location.protocol !== 'https:' ? tx('当前兼容 HTTP 写入，账号、密码和管理口令在传输中未加密', 'HTTP writes are currently allowed; account, password, and management token are not encrypted in transit') : credentialTransportReady ? tx('管理口令仅保留在当前页面内存', 'Management token remains only in this page memory') : tx('当前 HTTP 地址只读；请使用 HTTPS 或 SSH 本地端口转发', 'This HTTP address is read-only; use HTTPS or an SSH local port forward')}</small></div><Badge value={nodeAccess?.insecure_http_allowed && window.location.protocol !== 'https:' ? 'HTTP COMPATIBILITY / UNSAFE' : !credentialTransportReady ? 'SECURE TRANSPORT REQUIRED' : nodeAccess?.management_ready ? 'WRITE PROTECTED' : 'MANAGEMENT OFFLINE'} kind={nodeAccess?.insecure_http_allowed && window.location.protocol !== 'https:' ? 'warning' : credentialTransportReady && nodeAccess?.management_ready ? 'info' : 'warning'} /></div>
            <label>{tx('管理口令', 'Management token')}<input type="password" autoComplete="off" value={credentialAdminToken} onChange={event => setCredentialAdminToken(event.target.value)} /></label>
            <div className="credential-form-row"><label>Profile ID<input value={credentialDraft.profile_id} onChange={event => setCredentialDraft(current => ({ ...current, profile_id: event.target.value.toLowerCase() }))} placeholder="node-password-a" /></label><label>{tx('优先级', 'Priority')}<input type="number" min={1} max={10000} value={credentialDraft.priority} onChange={event => setCredentialDraft(current => ({ ...current, priority: Number(event.target.value) }))} /></label></div>
            <label>{tx('节点账号', 'Node username')}<input autoComplete="off" value={credentialDraft.username} onChange={event => setCredentialDraft(current => ({ ...current, username: event.target.value }))} /></label>
            <label>{tx('节点密码', 'Node password')}<input type="password" autoComplete="new-password" value={credentialDraft.password} onChange={event => setCredentialDraft(current => ({ ...current, password: event.target.value }))} /></label>
            <label className="training-check"><input type="checkbox" checked={credentialDraft.enabled} onChange={event => setCredentialDraft(current => ({ ...current, enabled: event.target.checked }))} /><span>{tx('启用并加入认证尝试顺序', 'Enable and include in authentication order')}</span></label>
            {credentialError && <p className="form-error">{credentialError}</p>}{credentialMessage && <p className="form-success">{credentialMessage}</p>}
            <button className="primary-action" type="submit" disabled={credentialSaving || !credentialTransportReady || !nodeAccess?.management_ready || !credentialAdminToken || !credentialDraft.profile_id || !credentialDraft.username || !credentialDraft.password}><Save size={15} />{credentialSaving ? tx('加密保存中', 'Encrypting') : tx('加密保存', 'Encrypt & Save')}</button>
          </form>
          <div className="table-wrap credential-table-wrap"><table className="node-access-table"><thead><tr><th>{tx('顺序', 'Order')}</th><th>PROFILE</th><th>{tx('账号', 'Username')}</th><th>{tx('认证方式', 'Auth type')}</th><th>{tx('密钥来源', 'Secret provider')}</th><th>{tx('状态', 'Status')}</th><th>{tx('操作', 'Action')}</th></tr></thead><tbody>{nodeAccess?.credential_profiles.map(profile => <tr key={profile.id}><td><code>{profile.priority}</code></td><td><b>{profile.id}</b></td><td><code>{profile.username_masked}</code></td><td>{profile.auth_type}</td><td><Badge value={profile.secret_provider.toUpperCase()} kind="info" /></td><td><Badge value={profile.status.toUpperCase()} kind={profile.status === 'ready' ? 'healthy' : profile.status === 'secret_unavailable' ? 'warning' : 'neutral'} /></td><td><button className="danger-icon" type="button" disabled={!credentialTransportReady || !credentialAdminToken} onClick={() => void deleteCredential(profile.id)} title={tx('删除凭据配置', 'Delete credential profile')}><Trash2 size={14} /></button></td></tr>)}</tbody></table>{!nodeAccess?.credential_profiles.length && <Empty tx={tx} title={tx('尚未配置加密凭据', 'No encrypted credentials configured')} />}</div>
        </div>
      </Card>
      <Card className="span-12 connectivity-card">
        <CardHead code="KNOWN-HOST SSH" title={tx('节点认证连通性检查', 'Node Authentication Connectivity Check')} action={<div className="table-actions"><Badge value={nodeAccess?.known_hosts_ready ? 'KNOWN HOSTS READY' : 'KNOWN HOSTS MISSING'} kind={nodeAccess?.known_hosts_ready ? 'healthy' : 'warning'} /><Badge value={`${connectivityChecks.length} RECENT`} kind="info" /></div>} />
        <p className="node-access-note">{tx('仅检查目标是否属于 Atlas 当前资产、SSH 主机指纹是否匹配以及凭据能否认证。不会执行 hostname、日志查询或任何其他命令；失败会进入 access 分类问题台账，后续成功会自动恢复。', 'Checks only whether the target is a current Atlas asset, its SSH host key matches, and a credential authenticates. It runs no hostname, log, or other command; failures enter the access issue ledger and a later success clears them automatically.')}</p>
        <div className="connectivity-runner">
          <label>{tx('节点 IP', 'Node IP')}<input value={checkNodeIP} onChange={event => setCheckNodeIP(event.target.value)} placeholder="10.114.4.25" /></label>
          <button className="primary-action" type="button" onClick={() => void runConnectivityCheck()} disabled={connectivityChecking || !credentialTransportReady || !credentialAdminToken || !nodeAccess?.connectivity_check_enabled || !nodeAccess?.known_hosts_ready || !nodeAccess?.credential_profiles.some(profile => profile.secret_available) || !checkNodeIP}><Network size={15} />{connectivityChecking ? tx('检查中', 'Checking') : tx('只检查认证', 'Check Authentication')}</button>
        </div>
        {connectivityError && <p className="form-error">{connectivityError}</p>}
        <div className="table-wrap connectivity-table-wrap"><table className="node-access-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>{tx('结果', 'Result')}</th><th>PROFILE</th><th>{tx('尝试', 'Attempts')}</th><th>{tx('提醒', 'Alert')}</th><th>{tx('完成时间', 'Finished')}</th></tr></thead><tbody>{connectivityChecks.map(check => <tr key={check.id}><td><b>{check.node_ip}</b><small>#{check.id}</small></td><td><Badge value={check.status.toUpperCase()} kind={check.status === 'authenticated' ? 'healthy' : check.status === 'host_identity_failed' ? 'danger' : 'warning'} /></td><td><code>{check.credential_profile_id || '—'}</code></td><td><small>{(check.attempts || []).join(' · ') || '—'}</small></td><td><Badge value={check.alert_required ? 'REQUIRED' : 'NO'} kind={check.alert_required ? 'warning' : 'neutral'} /></td><td>{time(check.finished_at, lang)}</td></tr>)}</tbody></table>{connectivityChecks.length === 0 && <Empty tx={tx} title={tx('尚无节点认证检查记录', 'No node authentication checks yet')} />}</div>
      </Card>
      <Card className="span-7 node-command-card">
        <CardHead code="DEFAULT / READ-ONLY" title={tx('默认自动采集', 'Default Automatic Collection')} action={<Badge value={`${readOnly.length} COMMANDS`} kind="healthy" />} />
        <p className="node-access-note">{tx('系统状态、系统参数、GPU、PCIe、注册服务状态、限定时间窗日志与 BMC 只读信息均无需逐次计划或人工授权；采集器必须遵守固定超时、输出和并发预算。', 'System state, parameters, GPU, PCIe, registered service state, bounded logs, and read-only BMC evidence require no per-run plan or human authorization. The runner must enforce fixed timeout, output, and concurrency budgets.')}</p>
        <div className="node-command-list">{readOnly.map(command => <article key={command.id}><header><code>{command.id}</code><Badge value="DEFAULT" kind="healthy" /></header><b>{local(command.purpose)}</b><small>{command.preview}</small></article>)}</div>
      </Card>
      <Card className="span-5 node-command-card approval">
        <CardHead code="HUMAN CONFIRMATION" title={tx('必须指定节点和操作并确认', 'Exact Node and Action Confirmation')} action={<Badge value={`${approvalRequired.length} COMMANDS`} kind="warning" />} />
        <div className="node-command-list">{approvalRequired.map(command => <article key={command.id}><header><code>{command.id}</code><Badge value="APPROVAL" kind="warning" /></header><b>{local(command.purpose)}</b><small>{command.preview}</small></article>)}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="ENFORCED BOUNDARIES" title={tx('当前强制边界', 'Current Enforced Boundaries')} />
        <div className="rules">{(nodeAccess?.boundaries || []).map(item => <span key={item.en}><ShieldAlert size={15} />{local(item)}</span>)}</div>
      </Card>
    </div>;
  }
  if (view === 'identity') return <div className="grid"><Card className="span-12"><CardHead code="IDENTITY" title={tx('资产身份映射', 'Asset Identity')} /><div className="identity">{['HOST', 'BMC', 'CHASSIS', 'PCIE SLOT', 'BUS ID', 'GPU UUID'].map((x, i) => <span key={x}>{x}{i < 5 && <ChevronRight size={13} />}</span>)}</div></Card><Card className="span-12"><CardHead code="BMC / IPMI" title={tx('带外数据', 'Out-of-band Data')} /><div className="check-grid">{['SEL', 'PSU / VOLTAGE', 'FAN / THERMAL', 'POWER', 'PCIE SLOT', 'MEMORY CE / UE'].map(x => <span key={x}><CheckCircle2 size={14} />{x}</span>)}</div></Card></div>;
  if (view === 'audit') return <div className="grid">
    <Card className="span-12"><CardHead code="SYNC RUNS" title={tx('同步批次', 'Reconciliation Runs')} action={<Badge value={`${syncRuns.length} RUNS`} kind="info" />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('任务', 'Task')}</th><th>{tx('状态', 'Status')}</th><th>{tx('节点 / GPU / UUID', 'Nodes / GPUs / UUIDs')}</th><th>TARGETS</th><th>{tx('变化', 'Changes')}</th><th>{tx('开始时间', 'Started')}</th><th>{tx('耗时', 'Duration')}</th></tr></thead><tbody>{syncRuns.map(run => <tr key={run.id}><td><code>#{run.id}</code></td><td><b>{run.task_type || 'legacy'}</b></td><td><Badge value={run.status.toUpperCase()} kind={run.status === 'success' ? 'healthy' : run.status === 'failed' ? 'danger' : 'warning'} /></td><td>{run.node_count} / {run.gpu_count} / {run.known_uuid_count}</td><td>{run.target_count}</td><td>{run.change_count}</td><td>{time(run.started_at, lang)}</td><td>{run.finished_at ? `${Math.max(0, (new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1)}s` : '—'}</td></tr>)}</tbody></table>{syncRuns.length === 0 && <Empty tx={tx} title={tx('暂无同步批次', 'No sync runs')} />}</div></Card>
    <Card className="span-12"><CardHead code="CHANGE LOG" title={tx('资产变更', 'Asset Changes')} action={<Badge value={`${assetChanges.length} EVENTS`} kind={assetChanges.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('事件', 'Event')}</th><th>{tx('节点', 'Node')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('原值', 'Before')}</th><th>{tx('新值', 'After')}</th><th>RUN</th><th>{tx('时间', 'Time')}</th></tr></thead><tbody>{assetChanges.map(change => <tr key={change.id}><td><code>#{change.id}</code></td><td><Badge value={change.event_type.toUpperCase()} kind={change.event_type.includes('retired') ? 'danger' : 'warning'} /></td><td><b>{change.node_ip}</b></td><td><code>{change.asset_key}</code></td><td><code>{change.old_value || '—'}</code></td><td><code>{change.new_value || '—'}</code></td><td>#{change.sync_run_id || '—'}</td><td>{time(change.created_at, lang)}</td></tr>)}</tbody></table>{assetChanges.length === 0 && <Empty tx={tx} title={tx('暂无资产变化', 'No asset changes')} />}</div></Card>
  </div>;
  const problemTargets = targets.filter(target => target.health !== 'up');
  return <div className="grid"><Card className="span-12 capability-note"><Database size={19} /><div><CardHead code="MONITORING-ONLY" title={tx('监控数据质量发现', 'Monitoring Data Quality Detection')} /><p>{tx('无需登录节点即可基于 Prometheus 指标、Target、时序连续性和资产对账识别缺失、失效、漂移与覆盖异常。平台负责发现、定位和记录问题，不自动修改 exporter、监控配置或节点。', 'Without node access, Atlas can use Prometheus metrics, targets, time-series continuity and inventory reconciliation to detect missing, stale, drifting or uncovered telemetry. It identifies and records issues; it does not automatically modify exporters, monitoring configuration or nodes.')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>{definitions.map(([job, name, use]) => { const rows = targets.filter(x => x.job === job); const up = rows.filter(x => x.health === 'up').length; const down = rows.length - up; return <Card className="metric quality compact-quality-metric coverage-metric" key={job}><Server size={17} /><Badge value={inventoryError ? 'API ERROR' : down ? `${down} NOT UP` : rows.length ? 'ALL UP' : 'WAITING'} kind={inventoryError || down ? 'warning' : rows.length ? 'healthy' : 'neutral'} /><strong>{rows.length ? up : '—'}</strong><b>{name}</b><small>{use} · {rows.length || summary?.nodes.total || '—'} TARGETS</small></Card>; })}
    <Card className="span-12"><CardHead code="TARGET ISSUES" title={tx('非正常 Target', 'Non-UP Targets')} action={<Badge value={`${problemTargets.length} ISSUES`} kind={problemTargets.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>TARGET</th><th>{tx('状态', 'Health')}</th><th>{tx('原因', 'Reason')}</th><th>{tx('抑制', 'Suppression')}</th><th>{tx('原始错误', 'Last Error')}</th><th>{tx('同步时间', 'Synced')}</th></tr></thead><tbody>{problemTargets.map(target => <tr key={`${target.job}-${target.node_ip}`}><td><b>{target.node_ip}</b><small>{target.target_ip}</small></td><td><code>{target.job}</code></td><td><Badge value={target.health.toUpperCase()} kind={target.health === 'down' ? 'danger' : 'warning'} /></td><td><code>{target.reason_code || 'unclassified'}</code></td><td>{target.suppressed ? <Badge value={target.suppression_reason || 'SUPPRESSED'} kind="info" /> : <Badge value="ACTIONABLE" kind="warning" />}</td><td><small>{target.last_error || '—'}</small></td><td>{time(target.last_synced_at, lang)}</td></tr>)}</tbody></table>{problemTargets.length === 0 && <Empty tx={tx} title={tx('Target 全部正常', 'All targets healthy')} />}</div></Card>
  </div>;
}
function Models({ tx, view }: { tx: Tx; view: string }) { const layers = [['L1', tx('确定性规则', 'Deterministic rules'), 'health_score', tx('首期', 'ACTIVE')], ['L2', 'PyOD', 'anomaly_score', 'P3'], ['L3', tx('监督预测', 'Supervised prediction'), 'failure_probability', 'P4'], ['L4', 'LLM + Skill', 'RCA / SOP', 'P5']]; if (view === 'algorithms') return <div className="grid"><Card className="span-6"><CardHead code="PYOD" title={tx('候选算法', 'Candidate Algorithms')} /><div className="chips">{['ECOD', 'Isolation Forest', 'COPOD', 'PCA', 'HBOS'].map(x => <span key={x}>{x}</span>)}</div></Card><Card className="span-6"><CardHead code="GUARDRAILS" title={tx('上线约束', 'Release Gates')} /><div className="rules">{[tx('按时间与 GPU UUID 隔离训练/测试', 'Split train/test by time and GPU UUID'), tx('模型、特征、数据集版本化', 'Version model, features and dataset'), tx('未达门槛仅 shadow mode', 'Shadow mode before threshold'), tx('LLM 不修改原始分数', 'LLM cannot alter source scores')].map(x => <span key={x}><ShieldCheck size={15} />{x}</span>)}</div></Card></div>; return <div className="grid"><Card className="span-12"><CardHead code="DECISION STACK" title={tx('决策分层', 'Decision Stack')} /><div className="model-grid">{layers.map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1]}</b><code>{x[2]}</code><Badge value={x[3]} kind={x[0] === 'L1' ? 'healthy' : 'neutral'} /></div>)}</div></Card></div>; }
function About({ tx, view, platformConfig, onPlatformConfig }: { tx: Tx; view: string; platformConfig: PlatformConfig; onPlatformConfig: (config: PlatformConfig) => void }) {
  const [moduleDetailID, setModuleDetailID] = useState<string | null>(null);
  if (view === 'settings') return <PlatformSettings tx={tx} value={platformConfig} onSaved={onPlatformConfig} />;
  if (view === 'architecture') return <div className="grid">
    <Card className="span-12 architecture-map-card"><CardHead code="SYSTEM MAP" title={tx('平台模块架构', 'Platform Module Architecture')} action={<Badge value="MODULE RELATIONSHIPS" kind="info" />} /><img src="/atlas-platform-architecture.svg?v=20260721.1" alt={tx('ATLAS 平台模块架构图', 'ATLAS platform module architecture')} /></Card>
    <Card className="span-7"><CardHead code="ARCHITECTURE" title={tx('平台架构', 'Platform Architecture')} /><div className="architecture">{[['01', tx('采集', 'INGEST'), 'DCGM · Prometheus · logs · BMC'], ['02', tx('治理', 'NORMALIZE'), 'identity · event · feature'], ['03', tx('决策', 'DECIDE'), 'rules · PyOD · supervised'], ['04', tx('闭环', 'WORKFLOW'), 'alert · repair · validation']].map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1]}</b><code>{x[2]}</code></div>)}</div></Card>
    <Card className="span-5"><CardHead code="BOUNDARIES" title={tx('工程边界', 'Engineering Boundaries')} /><div className="rules danger-list">{[tx('无节点登录权限时仍可使用监控数据模式', 'Monitoring-only mode does not require node access'), tx('节点信息收集只能通过版本化 Skill 和注册命令', 'Node evidence collection requires a versioned Skill and registered commands'), tx('平台发现数据问题，不自动修复监控配置', 'Detect data issues; do not auto-remediate monitoring'), tx('未确认维护窗口时禁止主动压测', 'No active stress before maintenance confirmation'), tx('LLM 不直接评分', 'LLM does not score hardware'), tx('异常分不等于故障概率', 'Anomaly score is not failure probability'), tx('缺失数据输出 unknown', 'Missing data returns unknown'), tx('重启、隔离、复测需审批', 'Restart, isolation and validation require approval')].map(x => <span key={x}><AlertTriangle size={15} />{x}</span>)}</div></Card>
    <Card className="span-12"><CardHead code="VERSIONING" title={tx('模块化版本治理', 'Modular Version Governance')} /><div className="version-policy"><span><b>MAJOR</b>{tx('模块契约或核心语义不兼容变更', 'Incompatible module contract or semantics')}</span><span><b>MINOR</b>{tx('新增向后兼容能力', 'Backward-compatible capability')}</span><span><b>PATCH</b>{tx('规则、阈值、缺陷与展示修正', 'Rules, thresholds, fixes and presentation')}</span></div></Card>
  </div>;

  const modules = [
    { id: 'asset', name: tx('资产对账', 'Asset Reconciliation'), version: 'v0.2.1', status: tx('监控侧基线', 'MONITORING BASELINE'), desc: tx('以 LXOPEasier 的机房、机柜和设备资产为事实来源；Atlas 当前每 10 分钟以单飞任务同步 GPU 身份和 Target，后续通过接口同步事实资产。', 'LXOPEasier room, rack and device records are authoritative. Atlas currently reconciles GPU identity and targets every 10 minutes with a single-flight task; authoritative asset API sync remains pending.'), history: [tx('v0.2.1 · GPU 身份与 Target 合并为 10 分钟单飞对账，恢复状态同批更新', 'v0.2.1 · GPU identity and targets use one 10-minute single-flight reconciliation with same-run recovery'), tx('v0.2.0 · 明确 LXOPEasier 资产事实源；接口同步待接入', 'v0.2.0 · LXOPEasier established as source of truth; API sync pending'), tx('v0.1.2 · Target、身份增量与每日全量对账', 'v0.1.2 · target/identity incremental and daily full reconciliation'), tx('v0.1.1 · GPU 主机范围过滤与历史 UUID 恢复', 'v0.1.1 · GPU host filtering and historical UUID recovery')] },
    { id: 'quality', name: tx('监控数据质量发现', 'Monitoring Data Quality Detection'), version: 'v0.8.2', status: tx('采集覆盖间距校准', 'COVERAGE SPACING TUNED'), desc: tx('联合识别每卡连续性和采集链路问题，并在独立问题统计子页面按历史、已解决、遗留和当前检测统一展示。', 'Detect per-GPU continuity and collection-path issues, with unified discovered, resolved, remaining and active counts on a dedicated Issue Statistics subpage.'), history: [tx('v0.8.2 · 采集覆盖状态徽标与数值间距增加约 1.4 倍，卡片高度不变', 'v0.8.2 · increased coverage badge-to-value spacing by about 1.4× without changing card height'), tx('v0.8.1 · 压缩采集覆盖和指标连续性数据卡高度与留白', 'v0.8.1 · reduced target-coverage and metric-continuity card height and whitespace'), tx('v0.8.0 · 数据质量台账迁入独立问题统计子页，问题列表跳转保持筛选与分页一致', 'v0.8.0 · moved the quality ledger to a dedicated subpage with consistent issue-list filtering and pagination'), tx('v0.7.0 · 连续性问题接入系统数据质量分类统计和侧边栏遗留数', 'v0.7.0 · continuity findings joined System Data Quality category statistics and sidebar remaining count'), tx('v0.6.1 · Target 相对耗时改为审计项，不再单独产生问题', 'v0.6.1 · relative target duration became audit-only and no longer creates findings alone'), tx('v0.6.0 · 新增最大间隔、UUID 波动和 Target 抓取质量判定', 'v0.6.0 · maximum gap, UUID flaps and target scrape-quality decisions'), tx('v0.5.0 · 新增每卡 1h 样本存在率、当前样本年龄、连续性页面与问题闭环', 'v0.5.0 · per-GPU 1h presence, current sample age, continuity UI and issue lifecycle')] },
    { id: 'health', name: tx('硬件健康评分', 'Hardware Health Scoring'), version: 'v1.4.1', status: tx('结构质量增强', 'STRUCTURAL QUALITY PLUS'), desc: tx('评分快照携带样本连续性、UUID 波动和采集链路特征；结构异常进入数据质量而不误判为硬件故障。', 'Score snapshots carry sample continuity, UUID flap and collection-path features so structural anomalies enter data quality instead of hardware faults.'), history: [tx('v1.4.1 · 相对抓取耗时退出异常判定，保留审计观测', 'v1.4.1 · relative scrape duration left anomaly decisions and remains audit evidence'), tx('v1.4.0 · 接入最大间隔、UUID 波动和 DCGM Target 抓取质量', 'v1.4.0 · maximum gap, UUID flaps and DCGM target scrape quality'), tx('v1.3.0 · 健康快照接入每卡结构性可观测性特征', 'v1.3.0 · health snapshots consume per-GPU structural observability features'), tx('v1.2.0 · Correctable Row Remap 改为 1h/24h 增长判定，稳定累计值只观察', 'v1.2.0 · correctable row remaps use 1h/24h growth; stable totals are observation-only')] },
    { id: 'detection', name: tx('故障检测', 'Fault Detection'), version: 'v0.1.1', status: tx('规则与报告联动', 'RULE + REPORT LINKAGE'), desc: tx('识别已经发生或正在发生的 ECC、XID、掉卡、不可用、温度和 PCIe 等硬件故障，并将稳定事件身份交给只读证据与故障报告模块。', 'Detect ECC, XID, dropout, unavailable, thermal and PCIe hardware faults that have occurred or are occurring, then pass stable event identity to the read-only evidence and fault-report module.'), history: [tx('v0.1.1 · 硬件事件接入结构化证据与报告入口', 'v0.1.1 · hardware events linked to structured evidence and reports'), tx('v0.1.0 · Row Remap、温度、PCIe 与 XID 变化确定性规则', 'v0.1.0 · deterministic row-remap, thermal, PCIe and XID-change rules')] },
    { id: 'analysis', name: tx('只读证据与故障报告', 'Read-only Evidence & Fault Reports'), version: 'v0.1.0', status: tx('确定性报告基线', 'DETERMINISTIC REPORT BASELINE'), desc: tx('按事件聚合健康快照、Feature Catalog、规则命中、问题台账和人工处置，生成双语结构化故障报告。v0.1 不执行节点命令、不调用 LLM、不修改任务、节点或配置。', 'Aggregate health snapshots, Feature Catalog data, rule hits, issue records and operator resolutions by event into bilingual structured fault reports. v0.1 runs no node commands or LLM and changes no workloads, nodes or configuration.'), history: [tx('v0.1.0 · Evidence Bundle、确定性报告 API、证据引用、缺口与安全边界', 'v0.1.0 · Evidence Bundle, deterministic report APIs, evidence references, gaps and safety boundaries'), tx('v0.1.0 · 告警中心增加报告入口与双语报告抽屉', 'v0.1.0 · report entry and bilingual report drawer added to Alert Center')] },
    { id: 'node-access', name: tx('节点证据 Skill', 'Node Evidence Skill'), version: 'v0.3.2', status: tx('默认只读采集策略', 'DEFAULT READ-ONLY POLICY'), desc: tx('低负载只读证据无需逐次计划或授权，按注册命令和固定资源预算默认采集；诊断、测试、重启、重置和任务终止必须指定节点与操作并人工确认。实际受控采集器仍待接入。', 'Low-impact read-only evidence requires no per-run plan or authorization and is collected by default through registered commands and fixed budgets. Diagnostics, tests, restarts, resets, and workload termination require an exact node and action plus human confirmation. The controlled runner remains pending.'), history: [tx('v0.3.2 · 移除逐次计划入口，明确低负载只读默认采集与高影响操作人工确认边界', 'v0.3.2 · removed per-run planning and clarified default low-impact read-only collection versus confirmation-gated high-impact operations'), tx('v0.3.1 · 建立受管节点、注册命令与资源预算约束，后由默认采集策略替代页面计划', 'v0.3.1 · established managed-node, registered-command, and resource-budget constraints, later superseded by the default collection policy'), tx('v0.3.0 · 受管资产限制、known-host 校验、仅认证 API、审计记录和 access 问题自动开闭', 'v0.3.0 · managed-asset restriction, known-host verification, authentication-only API, audit records, and automatic access issue lifecycle'), tx('v0.2.1 · 增加默认关闭的 HTTP 兼容开关、API 状态与页面风险提示', 'v0.2.1 · added a default-off HTTP compatibility switch, API status, and UI risk warning'), tx('v0.2.0 · 页面加密录入、密文落库、管理口令写保护、掩码列表与认证前解密', 'v0.2.0 · encrypted page entry, ciphertext persistence, management-token write protection, masked lists, and pre-authentication decryption'), tx('v0.1.0 · Skill 契约、只读/审批命令分级、凭据引用轮换状态机与状态页面', 'v0.1.0 · Skill contract, read-only/approval command classes, credential-reference rotation state machine, and status UI')] },
    { id: 'skill-foundation', name: tx('基础 Skill 体系', 'Foundational Skill System'), version: 'v0.1.0', status: tx('契约就绪', 'CONTRACT READY'), desc: tx('建立节点只读证据、证据化故障分析和脱敏案例学习三项基础 Skill。当前只交付可审计契约，不提供维护、重启、节点变更或任务操作能力。', 'Establish three foundational Skills for read-only node evidence, evidence-linked fault analysis, and redacted case learning. This release delivers auditable contracts only and provides no maintenance, restart, node-change, or workload-operation capability.'), history: [tx('v0.1.0 · atlas-fault-analysis 定义证据引用、假设状态和结构化报告边界', 'v0.1.0 · atlas-fault-analysis defines evidence references, hypothesis states, and structured-report boundaries'), tx('v0.1.0 · atlas-case-learning 定义脱敏、质量门、episode 与实体隔离划分', 'v0.1.0 · atlas-case-learning defines redaction, quality gates, episodes, and entity-isolated splits'), tx('v0.1.0 · 与 atlas-node-evidence 组成采集→分析→学习基础链路', 'v0.1.0 · forms the evidence-to-analysis-to-learning foundation with atlas-node-evidence')] },
    { id: 'feature', name: tx('统一特征目录', 'Unified Feature Catalog'), version: 'v1.8.0', status: tx('历史基线运行中', 'HISTORICAL BASELINE LIVE'), desc: tx('在 35 个在线健康特征、53 个源查询和全量 metric-family 规则之上，按型号、负载档和不可变特征版本生成 7 天稳健历史基线，为异常检测、风险排序、预测和衰减检测提供统一分布基础。', 'Builds seven-day robust historical baselines by model, load bucket and immutable feature version on top of 35 health features, 53 source queries and fleet metric-family rules, providing one distribution foundation for anomaly detection, risk ranking, prediction and degradation.'), history: [tx('基线 v1.1.0 · 以正式 FeatureDefinition 注册时间建立数据 epoch，排除复用版本号的早期实验快照', 'Baseline v1.1.0 · establishes a data epoch from the production FeatureDefinition registration time and excludes earlier experimental snapshots that reused the version'), tx('v1.8.0 · 正式版本隔离、当前版本默认读取与刷新耗时审计', 'v1.8.0 · production version isolation, current-version reads and refresh-duration audit'), tx('v1.7.0 · 7 天稳健历史基线、成熟度、刷新任务与读取 API', 'v1.7.0 · seven-day robust baselines, maturity, refresh jobs and read API'), tx('v1.6.0 · metric-family 规则提升并发布为全量正式特征，保持不参与健康评分', 'v1.6.0 · promoted and published metric-family rule as a production fleet feature while keeping it out of health scoring'), tx('v1.5.0 · metric-family 单节点 canary 配置、shadow 契约与范围保护', 'v1.5.0 · single-node metric-family canary config, shadow contract and scope guard')] },
    { id: 'prediction', name: tx('硬件故障预警与预测', 'Hardware Early Warning & Failure Prediction'), version: 'v0.0.1', status: tx('特征底座就绪，模型未交付', 'FEATURE FOUNDATION READY'), desc: tx('承载故障发生前的风险预警与概率预测，覆盖 GPU，并按统一资产、特征和标签模型扩展至服务器、存储和网络硬件；当前仅完成统一特征底座，尚未交付 PyOD 或监督概率模型。', 'Provide pre-failure risk warnings and probability prediction for GPUs, extending through a common asset, feature and label model to server, storage and network hardware. The common feature foundation is ready; PyOD and supervised probability models are not yet delivered.'), history: [tx('v0.0.1 · Feature Catalog v1 可供异常检测、风险排序和监督训练消费', 'v0.0.1 · Feature Catalog v1 is consumable by anomaly detection, risk ranking and supervised training'), tx('v0.0.0 · 特征、标签、PyOD、监督模型与概率校准路线完成', 'v0.0.0 · feature, label, PyOD, supervised-model and probability-calibration roadmap')] },
    { id: 'degradation', name: tx('性能衰减识别', 'Performance Degradation Detection'), version: 'v0.2.0', status: tx('历史基线影子检测', 'HISTORICAL SHADOW BASELINE'), desc: tx('高负载 SM 时钟检测优先消费成熟的同型号 7 天历史基线，未成熟时以同节点或同型号集群实时中位数兜底；结果不影响健康分，不输出故障概率。', 'High-load SM-clock detection prefers mature same-model seven-day historical baselines and falls back to live same-node or same-model fleet medians. Results do not affect health scores or emit failure probabilities.'), history: [tx('v0.2.0 · 接入型号/负载/版本历史基线，保留实时同类兜底', 'v0.2.0 · model/load/version historical baselines with live peer fallback'), tx('v0.1.0 · 被动候选 API、同类中位数基线、证据/置信度与影子模式页面', 'v0.1.0 · passive candidate APIs, peer-median baseline, evidence/confidence and shadow-mode UI'), tx('v0.0.1 · 性能特征、SuperBench/DCGM 验证和基线契约', 'v0.0.1 · performance features, SuperBench/DCGM validation and baseline contract'), tx('v0.0.0 · 被动检测与主动验证安全门设计', 'v0.0.0 · passive detection and active-validation safety gates')] },
    { id: 'incident', name: tx('告警中心', 'Alert Center'), version: 'v0.3.1', status: tx('结构化接收增强', 'STRUCTURED INGESTION PLUS'), desc: tx('分层管理原始接收记录与 Atlas 硬件告警；兼容新旧飞书卡片，保留源事件时间、主机和定位标签，并对历史 RAW 记录只读回退解析。', 'Separate raw ingestion records and Atlas hardware alerts. Support legacy and current Feishu cards, preserve source event time, host and locator labels, and read-only reparse historical RAW records.'), history: [tx('v0.3.1 · 新版状态总览/定位标签/恢复卡片解析、历史 RAW 回退与完整通知', 'v0.3.1 · current status/locator/recovery card parsing, historical RAW fallback and complete notifications'), tx('v0.3.0 · 硬件告警关联处置台账并支持详情与人工处置', 'v0.3.0 · hardware alerts link to details and operator resolution workflows'), tx('v0.2.2 · 硬件事件稳定 ID 游标、服务端筛选与前端分页', 'v0.2.2 · stable hardware-event ID cursor, server-side filtering and UI pagination'), tx('v0.2.1 · 只读生产接收库、真实总数、游标分页与新鲜度', 'v0.2.1 · read-only production ingestion store, real totals, cursor pagination and freshness'), tx('v0.2.0 · 接收记录、硬件事件与故障案例分层', 'v0.2.0 · ingestion, hardware event and fault case layers'), tx('v0.1.0 · open / recovered 事件生命周期', 'v0.1.0 · open/recovered event lifecycle')] },
    { id: 'issue', name: tx('数据统计', 'Data Statistics'), version: 'v0.5.1', status: tx('分类展示优化', 'CATEGORY UI REFINED'), desc: tx('在统一分类统计和人工处置台账之上，输出经过完整性审核、直接标识脱敏和实体隔离划分的训练/评估数据集，并清晰区分可用性、资产、数据质量与节点访问问题。', 'Build on classified statistics and human resolution records to export training/evaluation data with completeness gates, direct-identifier redaction and entity-isolated splits, while clearly separating availability, inventory, data-quality, and node-access issues.'), history: [tx('v0.5.1 · 问题分类补充节点访问分类和协调尺寸的分类图标', 'v0.5.1 · added the node-access category and consistently sized category icons'), tx('v0.5.0 · atlas-issue-training-v1 增加稳定快照、默认脱敏、质量门、训练/评估划分和页面导出', 'v0.5.0 · atlas-issue-training-v1 added stable snapshots, default redaction, quality gates, train/evaluation splits and UI export'), tx('v0.4.0 · 新增按分类的解决、遗留和当前检测统计', 'v0.4.0 · resolved, remaining and active counts by category'), tx('v0.3.0 · 更名为数据统计，硬件故障迁入告警中心且保留底层处置台账', 'v0.3.0 · renamed Data Statistics; hardware faults moved to Alert Center with resolution records retained'), tx('v0.2.3 · 恢复节点的 GPU/Target 状态同批更新并自动退出遗留统计', 'v0.2.3 · recovered GPU/target state updates in one run and automatically leaves remaining totals'), tx('v0.2.2 · 退休节点历史 Target 问题自动清除并退出活跃统计', 'v0.2.2 · historical target issues on retired nodes are automatically cleared and removed from active analytics')] },
    { id: 'platform', name: tx('平台实例配置', 'Platform Instance Configuration'), version: 'v0.1.0', status: tx('配置基线完成', 'CONFIGURATION BASELINE'), desc: tx('通过平台概览维护实例名称、产品名称、产品副标题和环境标识，并统一驱动导航、面包屑、环境徽标与浏览器标题。', 'Manage the instance name, product name, product tagline and environment label from Platform Overview, consistently driving navigation, breadcrumbs, environment badges and the browser title.'), history: [tx('v0.1.0 · 数据库持久化、运行时读取、页面编辑与安全字段边界', 'v0.1.0 · database persistence, runtime reads, UI editing and public-field safety boundary')] },
    { id: 'validation', name: tx('维修验证闭环', 'Repair Validation Workflow'), version: 'v0.0.0', status: tx('方案阶段', 'DESIGNED'), desc: tx('记录人工维修反馈、根因、修复或更换结果，并通过识别、遥测、错误计数和性能复测验证重新上线。', 'Capture repair feedback, root cause and replacement results, then validate return to service through identity, telemetry, counters and performance checks.'), history: [tx('v0.0.0 · 状态机、维护窗口和验证门设计', 'v0.0.0 · state machine, maintenance window and validation gates')] },
  ];
  const milestones = [
    ['P0', tx('数据底座', 'Data Foundation'), tx('完成', 'COMPLETE')],
    ['P1', tx('GPU 健康', 'GPU Health'), tx('基线完成', 'BASELINE')],
    ['P2', tx('故障闭环', 'Incident Workflow'), tx('开发中', 'ACTIVE')],
    ['P2.5', tx('性能验证', 'Performance Validation'), tx('开发中', 'ACTIVE')],
    ['P3', tx('特征与异常检测', 'Features & Anomaly Detection'), tx('开发中', 'ACTIVE')],
    ['P3.5', tx('只读证据与自动分析', 'Read-only Evidence & Analysis'), tx('开发中', 'ACTIVE')],
    ['P4', tx('硬件预警与预测', 'Hardware Warning & Prediction'), tx('规划', 'PLANNED')],
  ];
  const selectedModule = modules.find(module => module.id === moduleDetailID) || null;
  return <>
  <div className="grid">
    <Card className="span-12 product-intro"><div><span>{platformConfig.product_name}</span><h2>Infrastructure Hardware Reliability Workbench</h2><p>{tx('ATLAS 是面向 GPU 集群并可扩展至服务器、存储和网络基础设施的硬件可靠性工作台，提供资产对账、监控数据质量发现、硬件健康评分、故障检测、只读证据与结构化故障报告、数据统计与处置、硬件故障预警与预测、性能衰减识别、告警中心以及维修验证闭环。', 'ATLAS is a hardware reliability workbench for GPU clusters, extensible to server, storage and network infrastructure. It provides asset reconciliation, monitoring data quality detection, hardware health scoring, fault detection, read-only evidence and structured fault reports, data analytics and resolution, hardware early warning and failure prediction, performance degradation analysis, an alert center and repair validation workflows.')}</p></div><Badge value="PLATFORM / v0.19.2" kind="info" /></Card>
    <Card className="span-12"><CardHead code="MILESTONES" title={tx('平台开发里程碑', 'Platform Development Milestones')} /><div className="platform-milestones">{milestones.map(([phase, name, status]) => <div key={phase}><code>{phase}</code><b>{name}</b><Badge value={status} kind={status === tx('完成', 'COMPLETE') || status === tx('基线完成', 'BASELINE') ? 'healthy' : status === tx('开发中', 'ACTIVE') ? 'info' : 'neutral'} /></div>)}</div></Card>
    <Card className="span-12"><CardHead code="CAPABILITY MODULES" title={tx('平台能力模块', 'Platform Capability Modules')} action={<Badge value={`${modules.length} MODULES`} kind="info" />} /><div className="capability-modules">{modules.map(module => <article key={module.id}><header><code>{module.id.toUpperCase()}</code><Badge value={module.status} kind={module.status === tx('开发中', 'ACTIVE') ? 'info' : module.status.includes(tx('完成', 'BASELINE')) || module.status.includes('BASELINE') ? 'healthy' : 'neutral'} /></header><h3>{module.name}</h3><p>{module.desc}</p><div className="module-version"><span>{tx('当前版本', 'CURRENT VERSION')}</span><strong>{module.version}</strong></div><div className="module-history"><span>{tx('最近迭代', 'LATEST ITERATIONS')}</span>{module.history.slice(0, 3).map(item => <small key={item}>{item}</small>)}<button className="module-history-more" onClick={() => setModuleDetailID(module.id)}>{module.history.length > 3 ? tx(`查看全部 ${module.history.length} 次迭代`, `View all ${module.history.length} iterations`) : tx('版本详情', 'Version details')}<ChevronRight size={13} /></button></div></article>)}</div></Card>
  </div>
  <AnimatePresence>{selectedModule && <motion.div className="module-modal-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="module-modal-bg" onClick={() => setModuleDetailID(null)} aria-label={tx('关闭版本详情', 'Close version details')} /><motion.section className="module-modal" role="dialog" aria-modal="true" aria-label={selectedModule.name} initial={{ y: 18, scale: .98 }} animate={{ y: 0, scale: 1 }} exit={{ y: 18, scale: .98 }}><header><div><code>{selectedModule.id.toUpperCase()}</code><h2>{selectedModule.name}</h2></div><button className="icon-btn" onClick={() => setModuleDetailID(null)} aria-label={tx('关闭', 'Close')}><X size={17} /></button></header><div className="module-modal-meta"><Badge value={selectedModule.status} kind={selectedModule.status.includes(tx('完成', 'BASELINE')) || selectedModule.status.includes('BASELINE') ? 'healthy' : 'neutral'} /><span>{tx('当前版本', 'CURRENT VERSION')} <strong>{selectedModule.version}</strong></span></div><p>{selectedModule.desc}</p><div className="module-modal-history"><span>{tx('完整迭代历史', 'FULL ITERATION HISTORY')}</span>{selectedModule.history.map((item, index) => <div key={item}><i /><small>{item}</small>{index === 0 && <Badge value={tx('最新', 'LATEST')} kind="info" />}</div>)}</div></motion.section></motion.div>}</AnimatePresence>
  </>;
}

function PlatformSettings({ tx, value, onSaved }: { tx: Tx; value: PlatformConfig; onSaved: (config: PlatformConfig) => void }) {
  const [draft, setDraft] = useState<PlatformConfig>(value);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const update = (field: keyof PlatformConfig, next: string) => setDraft(current => ({ ...current, [field]: next }));
  const save = async () => {
    setSaving(true); setMessage(''); setError('');
    try {
      const response = await fetch('/api/v1/platform-config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          instance_name: draft.instance_name, product_name: draft.product_name,
          product_tagline: draft.product_tagline, environment: draft.environment,
        }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setDraft(payload.data);
      onSaved(payload.data);
      setMessage(tx('配置已保存并即时生效', 'Configuration saved and applied'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const valid = [draft.instance_name, draft.product_name, draft.product_tagline, draft.environment].every(item => item.trim());
  return <div className="grid platform-settings">
    <Card className="span-7">
      <CardHead code="INSTANCE BRANDING" title={tx('平台展示配置', 'Platform Display Settings')} action={<Badge value="PUBLIC FIELDS ONLY" kind="info" />} />
      <p className="settings-intro">{tx('这些字段只控制当前 Atlas 实例的展示身份，不会修改产品能力、采集地址、数据库或其他敏感运行参数。', 'These fields only control the display identity of this Atlas instance. They do not change product capabilities, collectors, databases or sensitive runtime settings.')}</p>
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('实例名称', 'Instance name')}<small>instance_name · 80</small></span><input maxLength={80} value={draft.instance_name} onChange={event => update('instance_name', event.target.value)} placeholder={tx('例如：智元集群', 'Example: Zhiyuan Cluster')} /></label>
        <label><span>{tx('产品名称', 'Product name')}<small>product_name · 40</small></span><input maxLength={40} value={draft.product_name} onChange={event => update('product_name', event.target.value)} placeholder="ATLAS" /></label>
        <label><span>{tx('产品副标题', 'Product tagline')}<small>product_tagline · 80</small></span><input maxLength={80} value={draft.product_tagline} onChange={event => update('product_tagline', event.target.value)} placeholder="GPU RELIABILITY" /></label>
        <label><span>{tx('环境标识', 'Environment label')}<small>environment · 40</small></span><input maxLength={40} value={draft.environment} onChange={event => update('environment', event.target.value)} placeholder="PRODUCTION / 7077" /></label>
        {error && <p className="form-error">{error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('保存并应用', 'Save & Apply')}</button>
      </form>
    </Card>
    <Card className="span-5 settings-preview">
      <CardHead code="LIVE PREVIEW" title={tx('展示预览', 'Display Preview')} />
      <div className="brand-preview"><span className="brand-icon"><Layers3 size={20} /></span><div><b>{draft.instance_name || '—'}</b><small>{draft.product_name || '—'}</small></div></div>
      <div className="preview-environment"><i /><span><b>{draft.environment || '—'}</b><small>{draft.product_tagline || '—'}</small></span></div>
      <dl><div><dt>{tx('浏览器标题', 'Browser title')}</dt><dd>{draft.instance_name || '—'} · {draft.product_name || '—'}</dd></div><div><dt>{tx('生效范围', 'Applied to')}</dt><dd>{tx('侧边栏、面包屑、环境徽标、浏览器标题', 'Sidebar, breadcrumbs, environment badge and browser title')}</dd></div></dl>
      {value.updated_at && <small className="settings-updated">{tx('最近更新', 'Last updated')} · {time(value.updated_at)}</small>}
    </Card>
  </div>;
}

function FaultEventList({ tx, items }: { tx: Tx; items: FaultEvent[] }) { return <div className="event-list">{items.map(x => <div className="event-row" key={x.id}><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /><span><b>{x.rule_code}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.evidence}</small></span><code>×{x.occurrence_count}</code></div>)}{items.length === 0 && <Empty tx={tx} title={tx('无未恢复硬件事件', 'No open hardware events')} />}</div>; }
function Empty({ tx, title }: { tx: Tx; title: string }) { return <div className="empty"><Command size={20} /><b>{title}</b><span>{tx('等待数据', 'Waiting for data')}</span></div>; }

function GlobalSearch({ tx, query, setQuery, pagesCopy, items, assets, close, navigate, select }: { tx: Tx; query: string; setQuery: (v: string) => void; pagesCopy: ReturnType<typeof pageCopy>; items: Ingestion[]; assets: GPUAsset[]; close: () => void; navigate: (p: PageId) => void; select: (id: number) => void }) {
  const input = useRef<HTMLInputElement>(null); useEffect(() => { input.current?.focus(); const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') close(); }; window.addEventListener('keydown', escape); return () => window.removeEventListener('keydown', escape); }, [close]);
  const q = query.trim().toLowerCase(); const pageRows = pages.filter(x => !q || `${pagesCopy[x].label} ${pagesCopy[x].title} ${x}`.toLowerCase().includes(q));
  const eventRows = q ? items.filter(x => [x.message, x.host, x.source, ...Object.entries(x.labels || {}).flat()].join(' ').toLowerCase().includes(q)).slice(0, 8) : items.slice(0, 4);
  const assetRows = q ? assets.filter(x => [x.node_ip, x.gpu_uuid, x.model_name, x.model, x.pci_bus_id, x.asset_key].join(' ').toLowerCase().includes(q)).slice(0, 8) : [];
  return <motion.div className="modal-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="modal-bg" onClick={close} aria-label={tx('关闭搜索', 'Close search')} /><motion.div className="search-modal" initial={{ y: -18, scale: .98 }} animate={{ y: 0, scale: 1 }}><div className="search-input"><Search size={18} /><input ref={input} value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('搜索页面、主机、GPU、UUID、XID、事件', 'Search pages, hosts, GPUs, UUIDs, XIDs, incidents')} /><kbd>ESC</kbd><button onClick={close} aria-label={tx('关闭搜索', 'Close search')}><X size={16} /></button></div><div className="search-results"><label>{tx('页面', 'PAGES')}</label>{pageRows.map(id => { const Icon = pageIcons[id]; return <button key={id} onClick={() => { navigate(id); close(); }}><Icon size={16} /><span><b>{pagesCopy[id].label}</b><small>{pagesCopy[id].desc}</small></span><ChevronRight size={14} /></button>; })}{assetRows.length > 0 && <label>{tx('GPU 资产', 'GPU ASSETS')}</label>}{assetRows.map(x => <button key={x.asset_key} onClick={() => { navigate('gpus'); close(); }}><Cpu size={16} /><span><b>{x.gpu_uuid || 'UUID UNKNOWN'}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.model_name || x.model || 'UNKNOWN'}</small></span><Badge value={x.state.toUpperCase()} kind={x.state === 'active' ? 'healthy' : 'warning'} /></button>)}<label>{tx('事件', 'INCIDENTS')}</label>{eventRows.map(x => <button key={x.id} onClick={() => select(x.id)}><ShieldAlert size={16} /><span><b>{x.message}</b><small>{x.host} · {x.labels?.UUID || x.source}</small></span><Badge value={x.level} kind={tone(x.level)} /></button>)}{pageRows.length + assetRows.length + eventRows.length === 0 && <Empty tx={tx} title={tx('无结果', 'No results')} />}</div></motion.div></motion.div>;
}

function IssueDrawer({ tx, detail, lang, close, saved }: { tx: Tx; detail: IssueDetail; lang: string; close: () => void; saved: () => void }) {
  const issue = detail.issue;
  const [status, setStatus] = useState(issue.status === 'open' ? 'in_progress' : issue.status);
  const [rootCause, setRootCause] = useState('');
  const [solution, setSolution] = useState('');
  const [process, setProcess] = useState('');
  const [result, setResult] = useState('');
  const [operator, setOperator] = useState('');
  const [trainingEligible, setTrainingEligible] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const submit = async () => {
    setSaving(true); setError('');
    try {
      const response = await fetch(`/api/v1/issues/${issue.id}/resolution`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status, root_cause: rootCause, solution, resolution_process: process, result, operator, training_eligible: trainingEligible, evidence: [] }) });
      if (!response.ok) { const payload = await response.json().catch(() => null); throw new Error(payload?.error?.message || `HTTP ${response.status}`); }
      setRootCause(''); setSolution(''); setProcess(''); setResult(''); setTrainingEligible(false); saved();
    } catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setSaving(false); }
  };
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer issue-drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>ISSUE / #{issue.id} · {issue.category.toUpperCase()}</span><h2>{issue.title}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /><time>{time(issue.last_detected_at, lang)}</time></div><div className="detail-grid">{[[tx('节点', 'Node'), issue.node_ip], ['GPU UUID', issue.gpu_uuid], [tx('对象', 'Entity'), `${issue.entity_type} / ${issue.entity_key}`], [tx('检测来源', 'Detection Source'), issue.detection_source]].map(item => <div key={item[0]}><small>{item[0]}</small><b>{item[1] || '—'}</b></div>)}</div><section><CardHead code="EVIDENCE" title={tx('问题描述', 'Problem Evidence')} /><p className="muted">{issue.description || '—'}</p></section><section><CardHead code="HUMAN FEEDBACK" title={tx('补充原因与解决过程', 'Root Cause & Resolution')} /><div className="resolution-form"><label>{tx('处理状态', 'Status')}<select value={status} onChange={event => setStatus(event.target.value)}><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><label>{tx('根本原因', 'Root cause')}<textarea value={rootCause} onChange={event => setRootCause(event.target.value)} placeholder={tx('问题为什么发生？需写证据，不只写现象', 'Why did it happen? Record evidence, not only symptoms.')} /></label><label>{tx('解决方案', 'Solution')}<textarea value={solution} onChange={event => setSolution(event.target.value)} placeholder={tx('采用了什么修复方案？', 'What remediation was chosen?')} /></label><label>{tx('解决过程', 'Resolution process')}<textarea value={process} onChange={event => setProcess(event.target.value)} placeholder={tx('按顺序记录操作、验证与回滚点', 'Record actions, validation and rollback points in order.')} /></label><label>{tx('处理结果', 'Result')}<textarea value={result} onChange={event => setResult(event.target.value)} placeholder={tx('是否恢复、如何验证、是否遗留风险', 'Recovery state, validation evidence and remaining risk.')} /></label><label>{tx('处理人', 'Operator')}<input value={operator} onChange={event => setOperator(event.target.value)} placeholder={tx('姓名或工号', 'Name or operator ID')} /></label><label className="training-check"><input type="checkbox" checked={trainingEligible} onChange={event => { setTrainingEligible(event.target.checked); if (event.target.checked) setStatus('resolved'); }} /><span>{tx('内容完整且已脱敏，可进入 AI/Skill 训练数据集', 'Complete and sanitized; eligible for AI/Skill training dataset')}</span></label>{error && <p className="form-error">{error}</p>}<button className="primary-action" onClick={submit} disabled={saving}>{saving ? tx('保存中…', 'Saving…') : tx('保存处置记录', 'Save resolution')}</button></div></section><section><CardHead code="HISTORY" title={tx('处置历史', 'Resolution History')} action={<Badge value={`${detail.resolutions.length} RECORDS`} kind="info" />} /><div className="resolution-history">{detail.resolutions.map(item => <article key={item.id}><header><Badge value={item.status.toUpperCase()} kind={issueTone(item.status)} /><b>{item.operator}</b><time>{time(item.created_at, lang)}</time>{item.training_eligible && <Badge value="TRAINING" kind="info" />}</header><p><strong>{tx('原因', 'Cause')}:</strong> {item.root_cause || '—'}</p><p><strong>{tx('方案', 'Solution')}:</strong> {item.solution || '—'}</p><p><strong>{tx('过程', 'Process')}:</strong> {item.resolution_process || '—'}</p><p><strong>{tx('结果', 'Result')}:</strong> {item.result || '—'}</p></article>)}{detail.resolutions.length === 0 && <p className="muted">{tx('尚无人工处置记录', 'No human resolution records yet')}</p>}</div></section></div></motion.aside></motion.div>;
}

function FaultReportDrawer({ tx, report, loading, error, lang, close }: { tx: Tx; report: FaultAnalysisReport | null; loading: boolean; error: string; lang: string; close: () => void }) {
  const zh = lang.startsWith('zh');
  const local = (value?: LocalizedText) => value ? (zh ? value.zh : value.en) : '—';
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
    <button className="drawer-bg" onClick={close} aria-label={tx('关闭报告', 'Close report')} />
    <motion.aside className="drawer fault-report-drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}>
      <header><div><span>FAULT REPORT / {report?.report_version || 'v0.1'}</span><h2>{report ? local(report.title) : tx('结构化故障报告', 'Structured Fault Report')}</h2></div><button className="icon-btn" onClick={close} aria-label={tx('关闭', 'Close')}><X size={17} /></button></header>
      <div className="drawer-body">
        {loading && <Empty tx={tx} title={tx('正在聚合只读证据', 'Aggregating read-only evidence')} />}
        {error && <div className="report-error"><AlertTriangle size={18} /><b>{tx('报告生成失败', 'Report generation failed')}</b><span>{error}</span></div>}
        {report && <>
          <div className="report-banner"><div><ShieldCheck size={18} /><span><b>{tx('只读确定性分析', 'Read-only deterministic analysis')}</b><small>{tx('仅聚合 Atlas 已有证据；未执行节点、任务、配置或重启操作。', 'Aggregates existing Atlas evidence only; no node, workload, configuration, or restart action was executed.')}</small></span></div><div><Badge value="DETERMINISTIC" kind="info" /><Badge value="READ-ONLY" kind="healthy" /><Badge value={report.no_action_executed ? 'NO ACTION EXECUTED' : 'ACTION UNKNOWN'} kind={report.no_action_executed ? 'healthy' : 'warning'} /></div></div>
          <div className="drawer-meta"><Badge value={report.severity.toUpperCase()} kind={tone(report.severity)} /><Badge value={report.analysis_mode.toUpperCase()} kind="info" /><time>{time(report.generated_at, lang)}</time></div>
          <div className="detail-grid">{[[tx('节点', 'Node'), report.affected_entity.node_ip], ['GPU', String(report.affected_entity.gpu_index)], ['GPU UUID', report.affected_entity.gpu_uuid], [tx('型号', 'Model'), report.affected_entity.model_name]].map(item => <div key={item[0]}><small>{item[0]}</small><b>{item[1] || '—'}</b></div>)}</div>
          <section><CardHead code="SUMMARY" title={tx('报告摘要', 'Executive Summary')} /><p className="muted">{local(report.summary)}</p></section>
          <section><CardHead code="FINDINGS" title={tx('确定性发现', 'Deterministic Findings')} action={<Badge value={`${report.findings.length} ITEMS`} kind="info" />} /><div className="report-items">{report.findings.map(item => <article key={item.code}><header><code>{item.code}</code><Badge value={item.severity.toUpperCase()} kind={tone(item.severity)} /></header><p>{local(item.summary)}</p><small>{item.evidence_ids.join(' · ')}</small></article>)}</div></section>
          <section><CardHead code="HYPOTHESES" title={tx('根因假设', 'Root-cause Hypotheses')} /><div className="report-items">{report.hypotheses.map(item => <article key={item.code}><header><b>{local(item.title)}</b><Badge value={item.status.toUpperCase()} kind={item.status === 'supported' ? 'healthy' : 'warning'} /></header><p>{local(item.reason)}</p><small>{item.evidence_ids.join(' · ')}</small></article>)}</div></section>
          <section><CardHead code="TIMELINE" title={tx('证据时间线', 'Evidence Timeline')} /><div className="report-timeline">{report.timeline.map(item => <div key={`${item.at}-${item.evidence_id}`}><time>{time(item.at, lang)}</time><i /><span><b>{local(item.label)}</b><small>{item.evidence_id}</small></span></div>)}</div></section>
          <section><CardHead code="EVIDENCE GAPS" title={tx('缺失证据', 'Missing Evidence')} action={<Badge value={`${report.missing_evidence.length} GAPS`} kind={report.missing_evidence.length ? 'warning' : 'healthy'} />} /><div className="report-gaps">{report.missing_evidence.map(item => <span key={item.code}><code>{item.code}</code>{local(item.detail)}</span>)}</div></section>
          <section><CardHead code="READ-ONLY CHECKS" title={tx('建议只读检查', 'Recommended Read-only Checks')} /><ol className="report-list">{report.recommended_readonly_checks.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ol></section>
          <section><CardHead code="OPERATOR" title={tx('人工后续动作', 'Operator Follow-up')} /><ol className="report-list">{report.operator_actions.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ol></section>
          <section><CardHead code="LIMITATIONS" title={tx('报告边界', 'Report Limitations')} /><ul className="report-list">{report.limitations.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ul></section>
        </>}
      </div>
    </motion.aside>
  </motion.div>;
}

function Drawer({ tx, item, report, lang, close }: { tx: Tx; item: Ingestion; report: Report | null; lang: string; close: () => void }) {
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>INCIDENT / #{item.id}</span><h2>{item.message}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={item.level} kind={tone(item.level)} /><span>{item.host || '—'}</span><time>{time(item.event_timestamp || item.created_at, lang)} · {item.event_timestamp ? tx('源事件时间', 'source event') : tx('接收时间', 'received')}</time></div><div className="detail-grid">{[['GPU UUID', item.labels?.UUID || item.labels?.uuid], ['PCI BUS ID', item.labels?.pci_bus_id], ['DEVICE', item.labels?.device || item.labels?.gpu], ['MODEL', item.labels?.modelName || item.labels?.model]].map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1] || '—'}</b></div>)}</div><section><CardHead code="ANALYSIS" title={tx('分析', 'Analysis')} action={report?.confidence != null ? <Badge value={`${Math.round(report.confidence * 100)}%`} kind="info" /> : undefined} />{report ? <div className="report"><p>{report.summary}</p>{report.probable_causes?.length ? <><b>{tx('可能原因', 'Probable causes')}</b><ul>{report.probable_causes.map(x => <li key={x}>{x}</li>)}</ul></> : null}{report.recommended_actions?.length ? <><b>{tx('处理建议', 'Actions')}</b><ol>{report.recommended_actions.map(x => <li key={x}>{x}</li>)}</ol></> : null}<small>{report.model} · DRAFT</small></div> : <p className="muted">{tx('无分析结果', 'No analysis result')}</p>}</section><section><CardHead code="DETAILS" title={tx('标签', 'Labels')} /><div className="labels">{Object.entries(item.labels || {}).map(([k, v]) => <div key={k}><small>{k}</small><b>{v}</b></div>)}</div></section><section><CardHead code="RAW" title="Payload" /><pre>{item.raw_payload || '—'}</pre></section></div></motion.aside></motion.div>;
}
