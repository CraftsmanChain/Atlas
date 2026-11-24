import { Component, Fragment, lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';
import {
  Activity, AlertTriangle, BarChart3, HardDrive, Settings2, BookOpen, CheckCircle2,
  ChevronLeft, ChevronRight, CircleGauge, ClipboardList, Command, Cpu, Database, Download, Eye, Filter, Gauge,
  Languages, Layers3, MemoryStick, Menu, Moon, Network,
  Palette, RefreshCw, Save, Search, Server, ShieldAlert, ShieldCheck, Sun,
  Thermometer, X, Zap,
} from 'lucide-react';
import './App.css';
const GPURisk = lazy(() => import('./GPURisk'));
const HardwareHealth = lazy(() => import('./HardwareHealth'));
import { pages, parseRoute, type PageId } from './routes';
import { readPlatformBrand, writePlatformBrand, fetchJSON, fetchAllPages } from './progressiveLoading';

type SubPage = { id: string; label: string };
type Tx = (zh: string, en: string) => string;
type ServerStatus = { status: string; version: string; commit: string; build_time: string; server_time: string };
type PlatformConfig = { instance_name: string; product_name: string; product_tagline: string; environment: string; updated_at?: string };
type LXOPAssetConfig = {
  auth_type: 'api_key' | 'oauth_client_credentials'; token_url: string;
  client_id_configured: boolean; client_secret_configured: boolean; ca_cert_pem: string;
  ops_host_url: string; asset_machine_url: string; data_center_id: string;
  insecure_skip_verify: boolean; enabled: boolean; api_key_configured: boolean;
  last_sync_status: string; last_sync_error?: string; last_sync_at?: string;
  last_ops_host_count: number; last_machine_count: number; updated_at?: string;
};
type ReconciliationSource = { asset_key: string; ip_address: string; name: string; type: string; model: string; state: string; sn: string; in_use: boolean };
type ReconciliationRow = {
  key: string; scope: 'both' | 'ops_only' | 'asset_only'; source_conflict?: boolean; category: string; type: string; gpu_model?: string;
  ip_address: string; name: string; sn: string; ops_host_records?: ReconciliationSource[]; asset_machine_records?: ReconciliationSource[]; ops_host?: ReconciliationSource; asset_machine?: ReconciliationSource;
};
type NamedCount = { name: string; count: number };
type ReconciliationSummary = {
  total: number; by_scope: Record<string, number>; by_category: NamedCount[];
  by_type: NamedCount[]; gpu_models: NamedCount[]; generated_at: string;
};
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
type IssueDetail = { issue: PlatformIssue; resolutions: IssueResolution[]; node_evidence_collection?: NodeEvidenceCollection | null };
type IssueSummary = { discovered: number; resolved: number; remaining: number; ignored: number; active_detection: number; training_eligible: number; by_category: Record<string, number>; resolved_by_category: Record<string, number>; remaining_by_category: Record<string, number>; active_by_category: Record<string, number>; by_status: Record<string, number>; by_severity: Record<string, number>; generated_at: string };
type TelemetryQualityItem = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; sample_count_1h?: number; presence_ratio_1h?: number; sample_age_seconds?: number; uuid_presence_flap_count_1h?: number; metric_gap_max_seconds_1h?: number; target_scrape_success_ratio_5m?: number; target_scrape_samples_ratio_5m?: number; target_scrape_duration_ratio_5m?: number; observed_at: string };
type TelemetryQualitySummary = { total: number; by_status: Record<string, number>; average_presence_ratio_1h: number; max_sample_age_seconds: number; max_metric_gap_seconds_1h?: number; max_uuid_flap_count_1h?: number; min_target_scrape_success_ratio_5m?: number; min_target_scrape_samples_ratio_5m?: number; max_target_scrape_duration_ratio_5m?: number; feature_catalog_version: string; evaluation_run_id: number; evaluated_at?: string };
type NodeEvidenceRecord = { id: number; collection_id: number; command_id: string; kind: string; status: string; output?: string; output_bytes: number; truncated: boolean; observed_at: string };
type NodeEvidenceCollection = {
  id: number; fault_event_id?: number; platform_issue_id?: number; retry_of_collection_id?: number;
  node_ip: string; trigger: string; status: string; credential_profile_id?: string;
  command_count: number; output_bytes: number; output_truncated: boolean; failure_code?: string;
  no_credential_disclosed: boolean; read_only: boolean; started_at: string; finished_at: string;
  records?: NodeEvidenceRecord[];
};
type DegradationSummary = { version: string; mode: string; evaluated_gpus: number; eligible_gpus: number; baseline_ready_gpus: number; historical_baseline_gpus: number; candidate_gpus: number; insufficient_gpus: number; minimum_utilization: number; ratio_threshold: number; freshness_sla_seconds: number; by_model: Record<string, number>; latest_observed_at?: string; evaluated_at: string };
type DegradationCandidate = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; metric: string; observed_value: number; baseline_value: number; performance_ratio: number; gpu_utilization: number; peer_count: number; baseline_id?: number; baseline_scope: string; baseline_maturity: string; data_confidence: string; evidence: string[]; recommended_action: string; feature_version: string; observed_at: string; evaluated_at: string };
type FleetSummary = {
  nodes: { total: number; by_state: Record<string, number> };
  gpus: { total: number; known_uuid: number; unknown_uuid: number; by_state: Record<string, number> };
  targets: { by_health: Record<string, number> };
  latest_sync: SyncRun | null;
};

const pageIcons = { overview: BarChart3, gpu: Cpu, memory: MemoryStick, disk: HardDrive, data: Database, settings: Settings2 };
const pageGroups: { id: string; zh: string; en: string; pages: PageId[] }[] = [
  { id: 'health', zh: '健康', en: 'HEALTH', pages: ['overview', 'gpu', 'memory', 'disk'] },
  { id: 'system', zh: '系统', en: 'SYSTEM', pages: ['data', 'settings'] },
];
const pageCopy = (tx: Tx): Record<PageId, { label: string; title: string; desc: string }> => ({
  overview: { label: tx('总览', 'Overview'), title: tx('总览', 'Overview'), desc: tx('GPU、内存、硬盘健康状态汇总', 'GPU, memory and disk health at a glance') },
  gpu: { label: 'GPU', title: 'GPU', desc: tx('GPU 卡与节点的健康、风险与事件', 'GPU and node health, risk and events') },
  memory: { label: tx('内存', 'Memory'), title: tx('内存', 'Memory'), desc: tx('GPU / CPU 算力节点的内存健康状态', 'Memory health of GPU / CPU compute nodes') },
  disk: { label: tx('硬盘', 'Disks'), title: tx('硬盘', 'Disks'), desc: tx('GPU / CPU 算力节点的硬盘健康状态', 'Disk health of GPU / CPU compute nodes') },
  data: { label: tx('数据采集', 'Data Collection'), title: tx('数据采集', 'Data Collection'), desc: tx('监控采集、资产对账与数据问题', 'Telemetry coverage, asset reconciliation and data issues') },
  settings: { label: tx('设置', 'Settings'), title: tx('设置', 'Settings'), desc: tx('平台、资产源与监控源', 'Platform, asset and monitoring sources') },
});
const subPages = (page: PageId, tx: Tx): SubPage[] => ({
  overview: [],
  gpu: [{ id: 'health', label: tx('健康', 'Health') }, { id: 'ranking', label: tx('风险排名', 'Risk Ranking') }, { id: 'events', label: tx('硬件事件', 'Hardware Events') }, { id: 'degradation', label: tx('性能衰减', 'Degradation') }, { id: 'inventory', label: tx('资产清单', 'Inventory') }],
  memory: [{ id: 'hits', label: tx('规则命中', 'Rule Hits') }, { id: 'list', label: tx('全部节点', 'All Nodes') }],
  disk: [{ id: 'hits', label: tx('规则命中', 'Rule Hits') }, { id: 'list', label: tx('全部磁盘', 'All Disks') }],
  data: [{ id: 'targets', label: tx('采集覆盖', 'Coverage') }, { id: 'continuity', label: tx('指标连续性', 'Continuity') }, { id: 'hardware', label: tx('硬件采集', 'Hardware Collection') }, { id: 'assets', label: tx('资产对账', 'Asset Reconciliation') }, { id: 'issues', label: tx('问题台账', 'Issue Ledger') }],
  settings: [{ id: 'platform', label: tx('平台配置', 'Platform') }, { id: 'audit', label: tx('资产核查', 'Asset Audit') }, { id: 'sync', label: tx('同步审计', 'Sync Audit') }],
}[page]);
const defaultSubPage: Record<PageId, string> = { overview: '', gpu: 'health', memory: 'hits', disk: 'hits', data: 'targets', settings: 'platform' };

function time(value?: string, lang = 'zh-CN') {
  if (!value) return '—';
  const date = new Date(value); if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(lang, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date);
}
function compareIP(left: string, right: string) { const a = left.split('.').map(Number); const b = right.split('.').map(Number); for (let i = 0; i < Math.max(a.length, b.length); i += 1) { const delta = (a[i] || 0) - (b[i] || 0); if (delta) return delta; } return left.localeCompare(right); }
function tone(level?: string) { const v = (level || '').toLowerCase(); return v === 'critical' || v === 'error' ? 'danger' : v === 'warning' || v === 'attention' ? 'warning' : v === 'info' ? 'info' : v === 'healthy' || v === 'recovered' ? 'healthy' : 'neutral'; }
function issueTone(status?: string) { const value = (status || '').toLowerCase(); return value === 'resolved' ? 'healthy' : value === 'in_progress' ? 'info' : value === 'open' ? 'warning' : 'neutral'; }
function Badge({ value, kind = 'neutral' }: { value: string; kind?: string }) { return <span className={`badge ${kind}`}><i />{value}</span>; }
function Card({ children, className = '', id }: { children: ReactNode; className?: string; id?: string }) { return <section id={id} className={`card ${className}`}>{children}</section>; }
function CardHead({ code, title, action }: { code?: string; title: string; action?: ReactNode }) { return <div className="card-head"><div>{code && <span>{code}</span>}<h2>{title}</h2></div>{action}</div>; }
class PageErrorBoundary extends Component<{ children: ReactNode; title: string }, { error: Error | null }> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('Atlas page render failed', error, info.componentStack); }
  render() {
    if (this.state.error) return <Card className="span-12"><CardHead code="RENDER ERROR" title={this.props.title} /><p className="node-access-note">{this.state.error.message || 'Unknown page rendering error'}</p></Card>;
    return this.props.children;
  }
}

// Polls only while the tab is visible; refreshes once when it becomes visible again.
function visibleInterval(run: () => void, ms: number) {
  let last = Date.now();
  const tick = () => { if (document.visibilityState === 'visible') { last = Date.now(); run(); } };
  const onVisible = () => { if (document.visibilityState === 'visible' && Date.now() - last >= ms) tick(); };
  const id = window.setInterval(tick, ms);
  document.addEventListener('visibilitychange', onVisible);
  return () => { window.clearInterval(id); document.removeEventListener('visibilitychange', onVisible); };
}
export default function App() {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const tx: Tx = useCallback((cn, en) => zh ? cn : en, [zh]);
  const copy = pageCopy(tx);
  const [initialPage, initialView] = parseRoute(window.location.hash);
  const [page, setPage] = useState<PageId>(initialPage);
  const [sidebar, setSidebar] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<ServerStatus | null>(null);
  const [platformConfig, setPlatformConfig] = useState<PlatformConfig>(() => readPlatformBrand({ instance_name: 'Atlas Cluster', product_name: 'ATLAS', product_tagline: 'GPU RELIABILITY', environment: 'LOCAL' }));
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
  const [refreshing, setRefreshing] = useState(false);
  const [level, setLevel] = useState('');
  const [issueSummary, setIssueSummary] = useState<IssueSummary | null>(null);
  const [issues, setIssues] = useState<PlatformIssue[]>([]);
  const [issueMeta, setIssueMeta] = useState<CursorMeta>({ total: 0, limit: 50, has_more: false, next_before_id: 0 });
  const [issueBeforeID, setIssueBeforeID] = useState(0);
  const [issueCursorHistory, setIssueCursorHistory] = useState<number[]>([]);
  const [issueCategory, setIssueCategory] = useState('');
  const [issueStatus, setIssueStatus] = useState('remaining');
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
  const [subPage, setSubPage] = useState<Record<PageId, string>>(() => initialView && subPages(initialPage, (zh2, en2) => zh2 || en2).some(item => item.id === initialView) ? { ...defaultSubPage, [initialPage]: initialView } : defaultSubPage);

  const navigate = (id: PageId, view?: string) => { const next = view || subPage[id]; if (view) setSubPage(current => ({ ...current, [id]: view })); setPage(id); setSidebar(false); window.location.hash = next ? `/${id}/${next}` : `/${id}`; window.scrollTo(0, 0); };
  const selectSubPage = (id: string) => { setSubPage(current => ({ ...current, [page]: id })); window.location.hash = `/${page}/${id}`; };
  useEffect(() => {
    const onHash = () => { const [p, v] = parseRoute(window.location.hash); setPage(p); if (v) setSubPage(current => ({ ...current, [p]: v })); };
    window.addEventListener('hashchange', onHash); return () => window.removeEventListener('hashchange', onHash);
  }, []);
  const load = useCallback(async (signal?: AbortSignal) => {
    setRefreshing(true);
    const jobs: Promise<void>[] = [];
    const read = (url: string, apply: (data: Awaited<ReturnType<typeof fetchJSON>>) => void, fail?: () => void) => {
      jobs.push(fetchJSON(url, signal).then(data => { if (!signal?.aborted) apply(data); }).catch(() => { if (!signal?.aborted) fail?.(); }));
    };
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const readAll = <T = any>(url: string, apply: (rows: T[]) => void, pageSize = 2000) => {
      jobs.push(fetchAllPages<T>(url, signal, pageSize).then(rows => { if (!signal?.aborted) apply(rows); }).catch(() => undefined));
    };
    read('/api/v1/status', setStatus);
    read('/api/v1/data-freshness', setFreshness);
    if (page === 'overview' || page === 'gpu' || page === 'data') {
      read('/api/v1/fleet/summary', data => { setSummary(data.data || null); setInventoryError(false); setLoading(false); }, () => { setInventoryError(true); setLoading(false); });
      if (page === 'overview' || subPage.gpu === 'health' || subPage.gpu === 'inventory') readAll<GPUAsset>('/api/v1/gpus', setGPUAssets);
    }
    if (page === 'overview' || page === 'gpu') {
      read('/api/v1/fault-events/summary', data => setFaultSummary(data.data || null));
    }
    if (page === 'gpu' || page === 'overview') read('/api/v1/health/summary', data => setHealthSummary(data.data || null));
    if (page === 'gpu' && subPage.gpu === 'health') readAll('/api/v1/health/gpus', setHealthScores, 1000);
    if (page === 'data' || page === 'settings') {
      readAll('/api/v1/targets', setTargets);
      read('/api/v1/sync-runs?limit=20', data => setSyncRuns(data.data || []));
      read('/api/v1/inventory/changes?limit=50', data => setAssetChanges(data.data || []));
      read('/api/v1/health/telemetry-quality', data => { setTelemetryQuality(data.data || []); setTelemetryQualitySummary(data.summary || null); });
    }
    if (page === 'data') read('/api/v1/issues/summary', data => setIssueSummary(data.data || null));
    if (page === 'gpu' && subPage.gpu === 'degradation') {
      read('/api/v1/degradation/summary', data => setDegradationSummary(data.data || null));
      read('/api/v1/degradation/candidates', data => setDegradationCandidates(data.data || []));
    }
    await Promise.allSettled(jobs);
    if (!signal?.aborted) setRefreshing(false);
  }, [page, subPage.gpu]);
  useEffect(() => {
    const controller = new AbortController();
    let busy = false;
    const refreshPage = async () => { if (busy) return; busy = true; await load(controller.signal); busy = false; };
    void refreshPage();
    const stop = visibleInterval(() => void refreshPage(), 60000);
    return () => { controller.abort(); stop(); };
  }, [load]);
  useEffect(() => {
    const controller = new AbortController();
    const update = () => { void fetchJSON('/api/v1/platform-config', controller.signal).then(payload => { if (!controller.signal.aborted && payload.data) setPlatformConfig(payload.data); }).catch(() => {}); };
    update(); const stop = visibleInterval(update, 300000);
    return () => { controller.abort(); stop(); };
  }, []);
  useEffect(() => { writePlatformBrand(platformConfig); }, [platformConfig]);
  useEffect(() => { document.title = `${platformConfig.instance_name} · ${platformConfig.product_name}`; }, [platformConfig]);
  useEffect(() => {
    let cancelled = false;
    if (page !== 'overview' && !(page === 'gpu' && subPage.gpu === 'events')) return;
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
  }, [page, subPage.gpu, faultBeforeID, query, level, issueRefresh]);
  useEffect(() => { setFaultBeforeID(0); setFaultCursorHistory([]); }, [query, level]);
  useEffect(() => {
    let cancelled = false;
    if (page !== 'data' || subPage.data !== 'issues') return;
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
  }, [page, subPage.data, issueBeforeID, issueCategory, issueStatus, issueQuery, issueRefresh]);
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

  const fleetModels = useMemo(() => {
    const counts = new Map<string, number>();
    gpuAssets.forEach(x => { const name = x.model_name || x.model || 'UNKNOWN'; counts.set(name, (counts.get(name) || 0) + 1); });
    return [...counts.entries()].map(([model, count]) => ({ model, short: model.replace(/^NVIDIA\s+/i, '').replace('GeForce ', ''), count })).sort((a, b) => b.count - a.count);
  }, [gpuAssets]);
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
  const nextFaultPage = () => { if (faultMeta.has_more && faultMeta.next_before_id) { setFaultCursorHistory(current => [...current, faultBeforeID]); setFaultBeforeID(faultMeta.next_before_id); } };
  const previousFaultPage = () => { if (faultCursorHistory.length) { const previous = faultCursorHistory[faultCursorHistory.length - 1]; setFaultCursorHistory(current => current.slice(0, -1)); setFaultBeforeID(previous); } };
  const nextIssuePage = () => { if (issueMeta.has_more && issueMeta.next_before_id) { setIssueCursorHistory(current => [...current, issueBeforeID]); setIssueBeforeID(issueMeta.next_before_id); } };
  const previousIssuePage = () => { if (issueCursorHistory.length) { const previous = issueCursorHistory[issueCursorHistory.length - 1]; setIssueCursorHistory(current => current.slice(0, -1)); setIssueBeforeID(previous); } };
  const refresh = () => { void load(); setIssueRefresh(value => value + 1); };

  return <div className="app">
    <aside className={`sidebar ${sidebar ? 'open' : ''}`}>
      <div className="brand"><span className="brand-icon"><Layers3 size={19} /></span><div><b>{platformConfig.instance_name}</b><small>{platformConfig.product_name}</small></div><button className="mobile-only icon-btn" onClick={() => setSidebar(false)}><X size={17} /></button></div>
      <button className="cluster-switch" onClick={() => navigate('settings', 'platform')}><span><i /> {platformConfig.environment}</span><small>{platformConfig.product_tagline}</small><ChevronRight size={14} /></button>
      <nav>{pageGroups.map(group => <div className="nav-group" key={group.id}><label>{tx(group.zh, group.en)}</label>{group.pages.map(id => { const Icon = pageIcons[id]; const count = id === 'gpu' ? faultSummary?.open_by_severity?.critical : id === 'data' ? issueSummary?.remaining : 0; return <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={16} /><span>{copy[id].label}</span>{(count || 0) > 0 && <em>{count}</em>}</button>; })}</div>)}</nav>
      <div className="sidebar-footer"><span><i className={status?.status === 'ok' ? 'online' : ''} />API {status?.status === 'ok' ? 'ONLINE' : 'WAITING'}</span><small>{status?.version || 'dev'} · {status?.commit || 'local'}</small></div>
    </aside>
    {sidebar && <button className="scrim" onClick={() => setSidebar(false)} />}
    <main>
      <header className="topbar">
        <button className="mobile-only icon-btn" onClick={() => setSidebar(true)}><Menu size={18} /></button>
        <div className="crumb"><span>{platformConfig.instance_name}</span><ChevronRight size={13} /><b>{copy[page].label}</b></div>
        <button className="global-search" onClick={() => setSearchOpen(true)}><Search size={15} /><span>{tx('搜索资源', 'Search resources')}</span><kbd>⌘ K</kbd></button>
        <div className="top-actions"><LanguageButton i18n={i18n} zh={zh} /><ThemeButton tx={tx} /><button className="icon-btn" onClick={refresh} title={tx('刷新', 'Refresh')}><RefreshCw size={16} className={refreshing ? 'spin' : ''} /></button></div>
      </header>
      <div className="content">
        <div className="page-head"><div><h1>{copy[page].title}</h1>{copy[page].desc && <p>{copy[page].desc}</p>}</div><div className="page-meta">{page === 'memory' || page === 'disk' ? <Badge value={tx('日更', 'DAILY')} kind="info" /> : <><Badge value={platformConfig.environment} kind="info" /><Badge value={`DATA ${freshnessStatus}`} kind={freshnessTone} />{collectionSources.length > 0 && <Badge value={overdueSources.length > 0 ? tx('采集超时', 'COLLECTION OVERDUE') : tx('采集中', 'COLLECTING')} kind={overdueSources.length > 0 ? 'warning' : 'info'} />}<span title={tx('源数据最新观测时间', 'Latest source observation')}>{freshnessLatest ? time(freshnessLatest, lang) : '—'}</span></>}</div></div>
        {page !== 'memory' && page !== 'disk' && overdueSources.length > 0 && <div className="collection-alert"><AlertTriangle size={17} /><div><b>{tx('监控采集超过 10 分钟未完成', 'Collection overdue > 10 min')}</b><span>{overdueSources.map(([name, source]) => `${name.toUpperCase()} ${Math.floor((source.collection_age_seconds || 0) / 60)}m`).join(' · ')}</span></div></div>}
        {currentSubPages.length > 0 && <nav className="subnav" aria-label={tx('页面分区', 'Page sections')}>{currentSubPages.map(item => <button key={item.id} className={subPage[page] === item.id ? 'active' : ''} onClick={() => selectSubPage(item.id)}>{item.label}</button>)}</nav>}
        <AnimatePresence mode="wait"><motion.div key={page} className={page === 'overview' ? 'overview-page' : 'secondary-page'} initial={{ opacity: 0, y: 5 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: .12 }}>
          {page === 'overview' && <Overview tx={tx} faults={openFaults} faultSummary={faultSummary} hosts={hosts} loading={loading} summary={summary} models={fleetModels} healthSummary={healthSummary} inventoryError={inventoryError} navigate={navigate} />}
          {page === 'gpu' && (subPage.gpu === 'health' || subPage.gpu === 'inventory') && <Gpus tx={tx} view={subPage.gpu} assets={gpuAssets} models={fleetModels} loading={loading} inventoryError={inventoryError} healthScores={healthScores} healthSummary={healthSummary} lang={lang} />}
          {page === 'gpu' && subPage.gpu === 'ranking' && <Suspense fallback={<p>{tx('加载中…', 'Loading…')}</p>}><PageErrorBoundary title={tx('GPU 风险排名读取失败', 'GPU ranking error')}><GPURisk tx={tx} /></PageErrorBoundary></Suspense>}
          {page === 'gpu' && subPage.gpu === 'events' && <HardwareEvents tx={tx} faultRows={faultEvents} faultMeta={faultMeta} faultPage={faultCursorHistory.length + 1} faultLoading={faultLoading} previousFaultPage={previousFaultPage} nextFaultPage={nextFaultPage} query={query} setQuery={setQuery} level={level} setLevel={setLevel} selectIssue={setSelectedIssueID} selectReport={setSelectedFaultReportID} lang={lang} />}
          {page === 'gpu' && subPage.gpu === 'degradation' && <Validations tx={tx} summary={degradationSummary} candidates={degradationCandidates} lang={lang} />}
          {page === 'memory' && <Suspense fallback={<div aria-busy="true">{tx('加载中…', 'Loading…')}</div>}><PageErrorBoundary title={tx('硬件快照读取失败', 'Hardware snapshot error')}><HardwareHealth tx={tx} lang={lang} kind="memory" view={subPage.memory} /></PageErrorBoundary></Suspense>}
          {page === 'disk' && <Suspense fallback={<div aria-busy="true">{tx('加载中…', 'Loading…')}</div>}><PageErrorBoundary title={tx('硬件快照读取失败', 'Hardware snapshot error')}><HardwareHealth tx={tx} lang={lang} kind="disk" view={subPage.disk} /></PageErrorBoundary></Suspense>}
          {page === 'data' && subPage.data === 'hardware' && <Suspense fallback={<div aria-busy="true">{tx('加载中…', 'Loading…')}</div>}><PageErrorBoundary title={tx('硬件快照读取失败', 'Hardware snapshot error')}><HardwareHealth tx={tx} lang={lang} kind="coverage" view={subPage.data} /></PageErrorBoundary></Suspense>}
          {page === 'data' && subPage.data === 'assets' && <AssetStatistics tx={tx} lang={lang} />}
          {page === 'data' && subPage.data === 'issues' && <Issues tx={tx} view="ledger" summary={issueSummary} rows={issues} meta={issueMeta} page={issueCursorHistory.length + 1} loading={issueLoading} category={issueCategory} setCategory={setIssueCategory} status={issueStatus} setStatus={setIssueStatus} query={issueQuery} setQuery={setIssueQuery} previousPage={previousIssuePage} nextPage={nextIssuePage} select={setSelectedIssueID} lang={lang} />}
          {page === 'data' && (subPage.data === 'targets' || subPage.data === 'continuity') && <Quality tx={tx} view={subPage.data} targets={targets} summary={summary} inventoryError={inventoryError} syncRuns={syncRuns} assetChanges={assetChanges} telemetry={telemetryQuality} telemetrySummary={telemetryQualitySummary} lang={lang} />}
          {page === 'settings' && subPage.settings === 'platform' && <PlatformSettings tx={tx} value={platformConfig} onSaved={setPlatformConfig} />}
          {page === 'settings' && subPage.settings === 'audit' && <PageErrorBoundary title={tx('资产核查页面异常', 'Asset audit page error')}><AssetAuditCheck tx={tx} lang={lang} /></PageErrorBoundary>}
          {page === 'settings' && subPage.settings === 'sync' && <Quality tx={tx} view="audit" targets={targets} summary={summary} inventoryError={inventoryError} syncRuns={syncRuns} assetChanges={assetChanges} telemetry={telemetryQuality} telemetrySummary={telemetryQualitySummary} lang={lang} />}
        </motion.div></AnimatePresence>
      </div>
    </main>
    <AnimatePresence>{searchOpen && <GlobalSearch tx={tx} query={query} setQuery={setQuery} pagesCopy={copy} assets={gpuAssets} close={() => setSearchOpen(false)} navigate={navigate} />}</AnimatePresence>
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

type HardwareFleetHead = { day: string; collected_at: string | null; coverage: { nodes_total: number; collected: number }; summary: { disk: { total: number; by_level: Record<string, number> }; memory: { nodes: number; dimms: number; by_level: Record<string, number> } } };
function useHardwareHead() {
  const [head, setHead] = useState<HardwareFleetHead | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    fetchJSON('/api/v1/hardware/fleet?summary=1', controller.signal).then(payload => { if (!controller.signal.aborted) setHead(payload.data || null); }).catch(() => { if (!controller.signal.aborted) setFailed(true); });
    return () => controller.abort();
  }, []);
  return { head, failed };
}
function StatusCard({ icon, label, value, unit, parts, note, onOpen }: { icon: ReactNode; label: string; value: string; unit?: string; parts: { label: string; value: number; tone: string }[]; note: string; onOpen: () => void }) {
  return <button type="button" className="card status-card" onClick={onOpen}>
    <header>{icon}<span>{label}</span><ChevronRight size={14} /></header>
    <strong>{value}{unit && <em>{unit}</em>}</strong>
    <div className="status-parts">{parts.map(part => <span key={part.label} className={part.value ? `tone-${part.tone}` : 'tone-zero'}><b>{part.value.toLocaleString()}</b>{part.label}</span>)}</div>
    <small>{note}</small>
  </button>;
}
function Overview({ tx, faults, faultSummary, loading, summary, models, healthSummary, inventoryError, navigate }: { tx: Tx; faults: FaultEvent[]; faultSummary: FaultEventSummary | null; hosts: number; loading: boolean; summary: FleetSummary | null; models: { model: string; short: string; count: number }[]; healthSummary: HealthSummary | null; inventoryError: boolean; navigate: (p: PageId, view?: string) => void }) {
  const { head, failed } = useHardwareHead();
  const gpuTotal = summary?.gpus.total;
  const level = (counts: Record<string, number> | undefined) => [
    { label: tx('严重', 'critical'), value: counts?.critical || 0, tone: 'danger' },
    { label: tx('警告', 'warning'), value: counts?.warning || 0, tone: 'warning' },
    { label: tx('关注', 'attention'), value: counts?.attention || 0, tone: 'info' },
  ];
  const healthyShare = healthSummary && healthSummary.scored ? ((healthSummary.by_level.healthy || 0) / healthSummary.scored * 100).toFixed(1) : '—';
  const coverage = head ? (head.coverage.collected / Math.max(1, head.coverage.nodes_total) * 100).toFixed(1) : '—';
  const hwNote = failed ? tx('硬件快照不可用', 'Hardware snapshot unavailable') : head ? tx(`${head.day} 采集`, `Collected ${head.day}`) : tx('加载中', 'Loading');
  return <div className="grid">
    <div className="span-12 status-grid">
      <StatusCard icon={<Cpu size={17} />} label={tx('GPU 健康', 'GPU health')} value={healthyShare} unit="%" parts={level(healthSummary?.by_level)} note={healthSummary ? tx(`${healthSummary.scored.toLocaleString()} 卡已评分 · ${healthSummary.unknown} 未知`, `${healthSummary.scored.toLocaleString()} scored · ${healthSummary.unknown} unknown`) : tx('加载中', 'Loading')} onOpen={() => navigate('gpu', 'health')} />
      <StatusCard icon={<MemoryStick size={17} />} label={tx('内存', 'Memory')} value={head ? head.summary.memory.nodes.toLocaleString() : '—'} unit={tx('节点', 'nodes')} parts={level(head?.summary.memory.by_level)} note={head ? tx(`${head.summary.memory.dimms.toLocaleString()} 根内存条 · ${head.day}`, `${head.summary.memory.dimms.toLocaleString()} DIMMs · ${head.day}`) : hwNote} onOpen={() => navigate('memory')} />
      <StatusCard icon={<HardDrive size={17} />} label={tx('硬盘', 'Disks')} value={head ? head.summary.disk.total.toLocaleString() : '—'} unit={tx('块', 'disks')} parts={level(head?.summary.disk.by_level)} note={hwNote} onOpen={() => navigate('disk')} />
      <StatusCard icon={<Database size={17} />} label={tx('硬件采集覆盖', 'Hardware coverage')} value={coverage} unit="%" parts={head ? [{ label: tx('已采集', 'collected'), value: head.coverage.collected, tone: 'healthy' }, { label: tx('未采集', 'missing'), value: head.coverage.nodes_total - head.coverage.collected, tone: 'warning' }] : []} note={head ? tx(`范围 ${head.coverage.nodes_total} 个算力节点`, `${head.coverage.nodes_total} compute nodes in scope`) : hwNote} onOpen={() => navigate('data', 'hardware')} />
    </div>
    <Card className="span-8"><CardHead code="GPU EVENTS" title={tx('未恢复 GPU 事件', 'Open GPU Events')} action={<span className="card-head-actions"><Badge value={`${faultSummary?.open ?? '—'} OPEN · ${faultSummary?.open_by_severity.critical || 0} CRITICAL`} kind={faultSummary?.open_by_severity.critical ? 'danger' : 'neutral'} /><button className="link" onClick={() => navigate('gpu', 'events')}>{tx('全部', 'View all')}<ChevronRight size={13} /></button></span>} /><FaultEventList tx={tx} items={faults.slice(0, 8)} /></Card>
    <Card className="span-4 fleet-card"><CardHead code="FLEET" title={tx('GPU 型号', 'GPU Models')} action={<Badge value={inventoryError ? tx('资产接口异常', 'INVENTORY ERROR') : loading ? tx('加载中', 'LOADING') : `${gpuTotal ?? '—'} GPU · ${summary?.nodes.total ?? '—'} ${tx('节点', 'NODES')}`} kind={inventoryError ? 'warning' : 'info'} />} /><div className="fleet-list">{models.map(x => <div key={x.model}><span><b>{x.short}</b><small>{x.model}</small></span><div><i style={{ width: `${gpuTotal ? x.count / gpuTotal * 100 : 0}%` }} /></div><strong>{x.count}</strong></div>)}{models.length === 0 && <Empty tx={tx} title={tx('无资产数据', 'No inventory data')} />}</div></Card>
  </div>;
}


function Issues({ tx, view, summary, rows, meta, page, loading, category, setCategory, status, setStatus, query, setQuery, previousPage, nextPage, select, lang }: { tx: Tx; view: string; summary: IssueSummary | null; rows: PlatformIssue[]; meta: CursorMeta; page: number; loading: boolean; category: string; setCategory: (value: string) => void; status: string; setStatus: (value: string) => void; query: string; setQuery: (value: string) => void; previousPage: () => void; nextPage: () => void; select: (id: number) => void; lang: string }) {
  if (view === 'assets') return <AssetStatistics tx={tx} lang={lang} />;
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
    <Card className="span-12"><CardHead code="CATEGORIES" title={tx('问题分类', 'Issue Categories')} action={<div className="table-actions"><Badge value={`${summary?.remaining || 0} REMAINING`} kind="info" /><Badge value={`${summary?.training_eligible || 0} TRAINING`} kind={(summary?.training_eligible || 0) > 0 ? 'healthy' : 'neutral'} /><a className="link training-export" href="/api/v1/issues/training-data?download=1" download><Download size={13} />{tx('导出训练数据', 'Export training data')}</a></div>} /><div className="issue-categories"><button className={!category ? 'active' : ''} onClick={() => setCategory('')}><span className="issue-category-icon"><Layers3 size={21} /></span><b>{tx('全部问题', 'All Issues')}</b><strong>{summary?.remaining || 0}</strong><small>remaining</small></button>{categories.map(([key, label, Icon]) => <button key={key} className={category === key ? 'active' : ''} onClick={() => setCategory(key)}><span className="issue-category-icon"><Icon size={21} /></span><b>{label}</b><strong>{summary?.remaining_by_category?.[key] || 0}</strong><small>{key}</small></button>)}</div></Card>
    <Card className="span-12"><div className="toolbar issue-toolbar"><label><Search size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder={tx('节点 / GPU / 类型 / 描述', 'Node / GPU / type / description')} /></label><label className="select"><Filter size={15} /><select value={status} onChange={event => setStatus(event.target.value)}><option value="">{tx('全部状态', 'All status')}</option><option value="remaining">{tx('遗留问题', 'REMAINING')}</option><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><span>{loading ? tx('同步中', 'Syncing') : `${start}–${end} / ${meta.total}`}</span></div><div className="table-wrap"><table className="issue-table"><thead><tr><th>{tx('分类 / 类型', 'Category / Type')}</th><th>{tx('问题', 'Issue')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('级别', 'Severity')}</th><th>{tx('处理状态', 'Workflow')}</th><th>{tx('检测状态', 'Detection')}</th><th>{tx('最近发现', 'Last Seen')}</th><th /></tr></thead><tbody>{rows.map(issue => <tr key={issue.id}><td><b>{categoryName(issue.category)}</b><small><code>{issue.issue_type}</code></small></td><td><b>{issue.title}</b><small>{issue.description || '—'}</small></td><td><b>{issue.node_ip || issue.entity_key}</b><small><code>{issue.gpu_uuid || issue.entity_type}</code></small></td><td><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /></td><td><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /></td><td><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /></td><td>{time(issue.last_detected_at, lang)}</td><td><button className="link" onClick={() => select(issue.id)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('没有匹配的问题', 'No matching issues')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${page} 页`, `Page ${page}`)}</span><div><button onClick={previousPage} disabled={page <= 1}><ChevronLeft size={15} /></button><button onClick={nextPage} disabled={!meta.has_more}><ChevronRight size={15} /></button></div></div></Card>
  </div>;
}

function AssetStatistics({ tx, lang }: { tx: Tx; lang: string }) {
  const [rows, setRows] = useState<ReconciliationRow[]>([]);
  const [summary, setSummary] = useState<ReconciliationSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scope, setScope] = useState('');
  const [category, setCategory] = useState('gpu');
  const [query, setQuery] = useState('');
  const [assetPage, setAssetPage] = useState(0);
  const initialLoadComplete = useRef(false);
  useEffect(() => {
    let cancelled = false;
    const loadAssets = async () => {
      if (!initialLoadComplete.current) setLoading(true);
      setError('');
      try {
        const response = await fetch('/api/v1/inventory/reconciliation?limit=1000');
        const payload = await response.json();
        if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
        if (!cancelled) { setRows(Array.isArray(payload.data) ? payload.data : []); setSummary(payload.summary || null); }
      } catch (reason) {
        if (!cancelled) setError(reason instanceof Error ? reason.message : tx('资产对账加载失败', 'Failed to load asset reconciliation'));
      } finally {
        if (!cancelled) {
          initialLoadComplete.current = true;
          setLoading(false);
        }
      }
    };
    void loadAssets();
    const stop = visibleInterval(() => void loadAssets(), 300000);
    return () => { cancelled = true; stop(); };
  }, []);
  const categoryLabels: Record<string, string> = {
    gpu: tx('GPU 设备', 'GPU Devices'), cpu: tx('CPU / 通用服务器', 'CPU / General Servers'), network: tx('交换机', 'Switches'), firewall: tx('UFM / 防火墙', 'UFM / Firewall'),
    cloud: tx('腾讯云设备', 'Tencent Cloud'), operations: tx('监控运维', 'Monitoring Operations'),
    storage: tx('存储设备', 'Storage'), other: tx('其他设备', 'Other'),
  };
  const maxCategory = Math.max(1, ...(summary?.by_category || []).map(item => item.count));
  const maxGPUModel = Math.max(1, ...(summary?.gpu_models || []).map(item => item.count));
  const visible = rows.filter(row => (!scope || (row.scope === scope && row.category === 'gpu')) && (!category || row.category === category) && (!query.trim() || [row.ip_address, row.name, row.sn, row.type, row.gpu_model].join(' ').toLowerCase().includes(query.trim().toLowerCase())));
  const pageCount = Math.max(1, Math.ceil(visible.length / 50));
  const currentPage = Math.min(assetPage, pageCount - 1);
  const pageRows = visible.slice(currentPage * 50, (currentPage + 1) * 50);
  const sourceLabel = (value: string) => value === 'both' ? tx('双边共有', 'In both') : value === 'ops_only' ? tx('仅运维主机', 'Ops only') : tx('仅资产设备', 'Asset only');
  return <div className="grid asset-statistics">
    <Card className="span-12 capability-note"><Layers3 size={19} /><div><CardHead code="LXOP CROSS VALIDATION" title={tx('实时在用资产分类与双接口交叉验证', 'Live In-use Asset Classification & Cross-validation')} /><p>{tx('SN 优先、IP 补充关联 · 仅统计在用资产', 'Matched by SN, then IP · in-use assets only')}</p></div><Badge value={loading ? 'SYNCING' : `${summary?.total || 0} IN USE`} kind={error ? 'warning' : 'info'} /></Card>
    <Card className="span-7 asset-category-chart">
      <CardHead code="DEVICE TYPES" title={tx('设备类型分布', 'Device Type Distribution')} action={category ? <button className="link" onClick={() => setCategory('')}>{tx('显示全部', 'Show all')}<X size={12} /></button> : undefined} />
      <div className="asset-bars">{(summary?.by_category || []).map(item => <button key={item.name} className={category === item.name ? 'active' : ''} onClick={() => setCategory(current => current === item.name ? '' : item.name)}><span><b>{categoryLabels[item.name] || item.name}</b><strong>{item.count}</strong></span><i><em style={{ width: `${Math.max(4, item.count / maxCategory * 100)}%` }} /></i></button>)}</div>
    </Card>
    <Card className="span-5 gpu-model-chart">
      <CardHead code="GPU MODELS" title={tx('GPU 型号分布', 'GPU Model Distribution')} action={<Badge value={`${(summary?.by_category || []).find(item => item.name === 'gpu')?.count || 0} GPU`} kind="healthy" />} />
      <div className="gpu-model-bars">{(summary?.gpu_models || []).map(item => <div key={item.name}><span><b>{item.name}</b><strong>{item.count}</strong></span><i><em style={{ width: `${Math.max(5, item.count / maxGPUModel * 100)}%` }} /></i></div>)}</div>
    </Card>
    <Card className="span-12 asset-difference-card">
      <CardHead code="SOURCE DIFFERENCE" title={tx('GPU 双接口差值', 'GPU Cross-source Difference')} action={scope ? <button className="link" onClick={() => setScope('')}>{tx('取消筛选', 'Clear filter')}<X size={12} /></button> : undefined} />
      <div className="source-diff-grid">
        {[['both', tx('双边共有', 'In both'), tx('SN 或 IP 匹配', 'Matched by SN or IP'), CheckCircle2], ['ops_only', tx('仅运维主机接口', 'Ops host only'), tx('当前资产源无匹配记录', 'No match in current machine source'), Server], ['asset_only', tx('仅资产设备接口', 'Asset machine only'), tx('当前运维源无匹配记录', 'No match in current ops source'), Database]].map(([key, label, note, Icon]) => { const I = Icon as typeof Server; const count = summary?.by_scope[String(key)] || 0; return <button key={String(key)} className={scope === key ? 'active' : ''} onClick={() => { setCategory('gpu'); setScope(current => current === key ? '' : String(key)); }}><I size={19} /><span><b>{String(label)}</b><small>{String(note)}</small></span><strong>{count}</strong></button>; })}
      </div>
    </Card>
    <Card className="span-12">
      <div className="toolbar asset-reconcile-toolbar"><label><Search size={15} /><input value={query} onChange={event => { setQuery(event.target.value); setAssetPage(0); }} placeholder={tx('IP / 名称 / SN / 类型 / GPU 型号', 'IP / name / SN / type / GPU model')} /></label><div className="table-actions"><span>{visible.length} / {rows.length}</span><button aria-label={tx('上一页资产', 'Previous asset page')} disabled={currentPage === 0} onClick={() => setAssetPage(currentPage - 1)}><ChevronLeft size={15} /></button><span>{currentPage + 1} / {pageCount}</span><button aria-label={tx('下一页资产', 'Next asset page')} disabled={currentPage + 1 >= pageCount} onClick={() => setAssetPage(currentPage + 1)}><ChevronRight size={15} /></button></div></div>
      {error && <p className="form-error">{error}</p>}
      <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th>{tx('对账结果', 'Result')}</th><th>{tx('分类 / 类型', 'Category / Type')}</th><th>{tx('设备', 'Device')}</th><th>{tx('运维主机接口', 'Ops Host API')}</th><th>{tx('资产设备接口', 'Asset Machine API')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{pageRows.map(row => <tr key={row.key}><td><Badge value={sourceLabel(row.scope)} kind={row.scope === 'both' ? 'healthy' : 'warning'} />{row.source_conflict && <details><summary>{tx('同 IP 存在重复源记录，查看 SN', 'Duplicate source records; view serials')}</summary>{(row.ops_host_records || []).map(record => <small key={record.asset_key}>{tx('运维', 'Ops')}: {record.sn || 'NO SN'} · {record.state || tx('状态未知', 'Unknown state')}</small>)}{(row.asset_machine_records || []).map(record => <small key={record.asset_key}>{tx('资产', 'Machine')}: {record.sn || 'NO SN'} · {record.state || tx('状态未知', 'Unknown state')}</small>)}</details>}</td><td><b>{categoryLabels[row.category] || row.category}</b><small>{row.gpu_model || row.type || 'UNKNOWN'}</small></td><td><b>{row.ip_address || row.name || '—'}</b><small>{row.name || '—'} · <code>{row.sn || 'NO SN'}</code></small></td><td>{row.ops_host ? <><Badge value="PRESENT" kind="healthy" /><small>{row.ops_host.ip_address || 'NO IP'} · {row.ops_host.state || tx('状态未知', 'Unknown state')}</small></> : <Badge value="MISSING" kind="warning" />}</td><td>{row.asset_machine ? <><Badge value="PRESENT" kind="healthy" /><small>{row.asset_machine.type || 'UNKNOWN'} · {row.asset_machine.state}</small></> : <Badge value="MISSING" kind="warning" />}</td><td>{time(summary?.generated_at, lang)}</td></tr>)}</tbody></table>{!loading && visible.length === 0 && <Empty tx={tx} title={tx('没有匹配的资产记录', 'No matching asset records')} />}</div>
    </Card>
  </div>;
}

type AssetAuditConfigView = {
  enabled: boolean; delivery_check_enabled: boolean; scan_enabled: boolean;
  delivery_sheet_url: string; feishu_app_id: string; feishu_app_secret_configured: boolean;
  notify_enabled: boolean; notify_always: boolean; notify_webhook_url: string; notify_secret_configured: boolean;
  ssh_passwords_configured: boolean;
  schedule_hour: number; timezone: string; scan_concurrency: number; scan_timeout_ms: number; updated_at?: string;
};
type AssetAuditDeliverySummary = { sheet_rows: number; delivered_rows: number; excluded_rows: number; excluded?: AssetAuditDeliveryExclusion[]; matched: number; missing_asset: number; ip_mismatch: number; sn_notice: number; asset_ip_count: number; generated_at: string };
type AssetAuditDeliveryExclusion = { row: number; hostname: string; ip_address: string; reason: string };
type AssetAuditDeliveryFinding = { row: number; hostname: string; ip_address: string; sn: string; delivered_at: string; issue: string; detail: string; asset_ip?: string; asset_sn?: string; telemetry_sn?: string };
type AssetAuditScanSubnet = { cidr: string; hosts: number; live: number };
type AssetAuditScanHost = { ip_address: string; subnet: string; status: string };
type AssetAuditScanSummary = { subnets: AssetAuditScanSubnet[]; hosts?: AssetAuditScanHost[]; subnet_count: number; known_asset_ips: number; delivery_sheet_ips: number; scanned_targets: number; live_count: number; port22_closed: number; ssh_login_failed?: number; marked?: number; unregistered: number; truncated: boolean; generated_at: string };
type AssetAuditScanFinding = { ip_address: string; subnet: string };
type AssetAuditMark = { ip_address: string; reason?: string; created_at?: string };
type AssetAuditRun = {
  id: number; kind: string; status: string; trigger: string; started_at: string; finished_at?: string;
  summary?: AssetAuditDeliverySummary | AssetAuditScanSummary | null;
  findings?: AssetAuditDeliveryFinding[] | AssetAuditScanFinding[] | null;
  notified: boolean; notify_error?: string; error_message?: string;
};
const emptyAssetAuditConfig: AssetAuditConfigView = {
  enabled: false, delivery_check_enabled: false, scan_enabled: false,
  delivery_sheet_url: '', feishu_app_id: '', feishu_app_secret_configured: false,
  notify_enabled: false, notify_always: false, notify_webhook_url: '', notify_secret_configured: false,
  ssh_passwords_configured: false,
  schedule_hour: 5, timezone: 'Asia/Shanghai', scan_concurrency: 1, scan_timeout_ms: 1000,
};
type MonitoringConfigView = { base_url: string; request_timeout: string; configured: boolean; effective_url?: string; updated_at?: string };
const emptyMonitoringConfig: MonitoringConfigView = { base_url: '', request_timeout: '15s', configured: false };
// The management token is entered once per browser session and shared by every
// write-protected card, so an operator never has to re-enter it on each page
// (and never sees a button that silently does nothing).
const ADMIN_TOKEN_STORAGE_KEY = 'atlas.admin-token';
function useAdminToken(): [string, (next: string) => void] {
  const [token, setToken] = useState<string>(() => { try { return window.sessionStorage.getItem(ADMIN_TOKEN_STORAGE_KEY) || ''; } catch { return ''; } });
  const update = useCallback((next: string) => {
    setToken(next);
    try {
      if (next) window.sessionStorage.setItem(ADMIN_TOKEN_STORAGE_KEY, next);
      else window.sessionStorage.removeItem(ADMIN_TOKEN_STORAGE_KEY);
    } catch { /* session storage unavailable; keep the value in memory only */ }
  }, []);
  return [token, update];
}
function AssetAuditSettings({ tx }: { tx: Tx }) {
  const [config, setConfig] = useState<AssetAuditConfigView>(emptyAssetAuditConfig);
  const [appSecret, setAppSecret] = useState('');
  const [notifySecret, setNotifySecret] = useState('');
  const [sshPasswords, setSshPasswords] = useState('');
  const [adminToken, setAdminToken] = useAdminToken();
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void fetch('/api/v1/asset-audit/config').then(async response => {
      const payload = await response.json();
      if (!response.ok || !payload.data) throw new Error(`HTTP ${response.status}`);
      if (!cancelled) { setConfig({ ...emptyAssetAuditConfig, ...payload.data }); setLoaded(true); }
    }).catch(() => { if (!cancelled) setError(tx('资产核查配置读取失败', 'Failed to load asset audit configuration')); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const update = <K extends keyof AssetAuditConfigView>(field: K, next: AssetAuditConfigView[K]) => setConfig(current => ({ ...current, [field]: next }));
  const save = async () => {
    setSaving(true); setMessage(''); setError('');
    if (!adminToken.trim()) { setSaving(false); setError(tx('请先填写管理口令', 'Enter the management token first')); return; }
    try {
      const response = await fetch('/api/v1/asset-audit/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({
          enabled: config.enabled, delivery_check_enabled: config.delivery_check_enabled, scan_enabled: config.scan_enabled,
          delivery_sheet_url: config.delivery_sheet_url, feishu_app_id: config.feishu_app_id, feishu_app_secret: appSecret,
          notify_enabled: config.notify_enabled, notify_always: config.notify_always, notify_webhook_url: config.notify_webhook_url, notify_secret: notifySecret,
          ssh_passwords: sshPasswords,
          schedule_hour: Number(config.schedule_hour), timezone: config.timezone,
          scan_concurrency: Number(config.scan_concurrency), scan_timeout_ms: Number(config.scan_timeout_ms),
        }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setConfig({ ...emptyAssetAuditConfig, ...payload.data });
      setAppSecret(''); setNotifySecret(''); setSshPasswords('');
      setMessage(tx('资产核查配置已加密保存', 'Asset audit configuration saved encrypted'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const trigger = async (kind: string) => {
    setMessage(''); setError('');
    if (!adminToken.trim()) { setError(tx('请先填写管理口令', 'Enter the management token first')); return; }
    setBusy(kind);
    try {
      const response = await fetch('/api/v1/asset-audit/trigger', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({ kind }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setMessage(tx('已触发，结果见“资产核查”', 'Triggered; see Asset Audit'));
    } catch (reason) {
      setBusy(''); setError(reason instanceof Error ? reason.message : tx('触发失败', 'Trigger failed'));
    } finally { setBusy(''); }
  };
  const valid = loaded && adminToken.trim() && config.timezone.trim() && config.schedule_hour >= 0 && config.schedule_hour <= 23
    && (!config.delivery_check_enabled || (config.delivery_sheet_url.trim() !== '' && config.feishu_app_id.trim() !== '' && (appSecret.trim() !== '' || config.feishu_app_secret_configured)));
  return <>
    <Card className="span-8 lxop-settings">
      <CardHead code="ASSET AUDIT" title={tx('资产核查与存活扫描', 'Asset Audit & Live-IP Scan')} action={<Badge value={config.enabled ? 'ENABLED' : 'DISABLED'} kind={config.enabled ? 'healthy' : 'neutral'} />} />
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('飞书表格 URL', 'Feishu sheet URL')}<small>{tx('交付资料表，需含 ?sheet= 参数', 'Delivery sheet; must include ?sheet=')}</small></span><input value={config.delivery_sheet_url} onChange={event => update('delivery_sheet_url', event.target.value)} placeholder="https://.../sheets/<token>?sheet=<id>" /></label>
        <label><span>{tx('飞书应用 App ID', 'Feishu app ID')}</span><input value={config.feishu_app_id} onChange={event => update('feishu_app_id', event.target.value)} /></label>
        <label><span>{tx('飞书应用 App Secret', 'Feishu app secret')}<small>{config.feishu_app_secret_configured ? tx('已加密保存 · 留空保持', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('用于读取表格', 'Used to read the sheet')}</small></span><input type="password" autoComplete="new-password" value={appSecret} onChange={event => setAppSecret(event.target.value)} placeholder={config.feishu_app_secret_configured ? '••••••••' : ''} /></label>
        <label><span>{tx('通知 Webhook', 'Notification webhook')}<small>{tx('飞书群机器人；留空回退全局机器人', 'Feishu group bot; blank falls back to the global bot')}</small></span><input value={config.notify_webhook_url} onChange={event => update('notify_webhook_url', event.target.value)} placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." /></label>
        <label><span>{tx('通知签名密钥', 'Webhook signing secret')}<small>{config.notify_secret_configured ? tx('已加密保存 · 留空保持', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('开启签名时填写', 'Fill when signing is enabled')}</small></span><input type="password" autoComplete="new-password" value={notifySecret} onChange={event => setNotifySecret(event.target.value)} /></label>
        <label><span>{tx('存活扫描 OS 密码', 'Live-scan OS passwords')}<small>{config.ssh_passwords_configured ? tx('已加密保存 · 留空保持', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('每行一条：用户名 密码；留空用内置默认凭据', 'One "username password" per line; blank uses the built-in defaults')}</small></span><textarea rows={3} autoComplete="new-password" value={sshPasswords} onChange={event => setSshPasswords(event.target.value)} /></label>
        <label><span>{tx('管理口令', 'Management token')}<small>X-Atlas-Admin-Token</small></span><input type="password" autoComplete="off" value={adminToken} onChange={event => setAdminToken(event.target.value)} /></label>
        <label><span>{tx('每日执行时刻', 'Daily schedule hour')}<small>{tx('0-23 · 时区', '0-23 · timezone')}</small></span><span className="check-options"><input type="number" min={0} max={23} value={config.schedule_hour} onChange={event => update('schedule_hour', Number(event.target.value))} /><input value={config.timezone} onChange={event => update('timezone', event.target.value)} placeholder="Asia/Shanghai" /></span></label>
        <label className="settings-check"><span>{tx('运行选项', 'Run options')}</span><span className="check-options"><input type="checkbox" checked={config.enabled} onChange={event => update('enabled', event.target.checked)} />{tx('启用资产核查', 'Enable asset audit')}<input type="checkbox" checked={config.delivery_check_enabled} onChange={event => update('delivery_check_enabled', event.target.checked)} />{tx('已交付节点核查', 'Delivered-node check')}<input type="checkbox" checked={config.scan_enabled} onChange={event => update('scan_enabled', event.target.checked)} />{tx('存活 IP 扫描', 'Live-IP scan')}<input type="checkbox" checked={config.notify_enabled} onChange={event => update('notify_enabled', event.target.checked)} />{tx('发送飞书通知', 'Send Feishu notice')}<input type="checkbox" checked={config.notify_always} onChange={event => update('notify_always', event.target.checked)} />{tx('无异常也通知', 'Notify even when clean')}</span></label>
        {error && <p className="form-error">{error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('加密保存', 'Save Encrypted')}</button>
      </form>
    </Card>
    <Card className="span-4 settings-preview lxop-status">
      <CardHead code="SCHEDULE" title={tx('执行状态', 'Run Status')} />
      <dl>
        <div><dt>{tx('自动执行', 'Automatic')}</dt><dd>{config.enabled ? tx(`每日 ${config.schedule_hour}:00`, `Daily ${config.schedule_hour}:00`) : tx('已停用', 'Disabled')}</dd></div>
        <div><dt>{tx('时区', 'Timezone')}</dt><dd>{config.timezone}</dd></div>
        <div><dt>{tx('已交付核查', 'Delivery check')}</dt><dd>{config.delivery_check_enabled ? tx('启用', 'On') : tx('停用', 'Off')}</dd></div>
        <div><dt>{tx('存活扫描', 'Live-IP scan')}</dt><dd>{config.scan_enabled ? tx('启用', 'On') : tx('停用', 'Off')}</dd></div>
        <div><dt>{tx('通知', 'Notification')}</dt><dd>{config.notify_enabled ? (config.notify_always ? tx('每次执行都通知', 'Every run') : tx('仅异常时通知', 'On anomalies only')) : tx('停用', 'Off')}</dd></div>
      </dl>
      <div className="table-actions audit-trigger-actions">
        <button className="primary-action" type="button" disabled={!!busy} onClick={() => void trigger('delivery_check')}><ClipboardList size={15} />{busy === 'delivery_check' ? tx('执行中', 'Running') : tx('立即核查', 'Run Check')}</button>
        <button className="primary-action" type="button" disabled={!!busy} onClick={() => void trigger('ip_scan')}><Network size={15} />{busy === 'ip_scan' ? tx('扫描中', 'Scanning') : tx('立即扫描', 'Run Scan')}</button>
      </div>
    </Card>
  </>;
}
function MonitoringSettings({ tx }: { tx: Tx }) {
  const [config, setConfig] = useState<MonitoringConfigView>(emptyMonitoringConfig);
  const [adminToken, setAdminToken] = useAdminToken();
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void fetch('/api/v1/platform-config/monitoring').then(async response => {
      const payload = await response.json();
      if (!response.ok || !payload.data) throw new Error(`HTTP ${response.status}`);
      if (!cancelled) { setConfig({ ...emptyMonitoringConfig, ...payload.data }); setLoaded(true); }
    }).catch(() => { if (!cancelled) setError(tx('监控数据源读取失败', 'Failed to load monitoring configuration')); });
    return () => { cancelled = true; };
  }, []);
  const save = async () => {
    setSaving(true); setMessage(''); setError('');
    if (!adminToken.trim()) { setSaving(false); setError(tx('请先填写管理口令', 'Enter the management token first')); return; }
    try {
      const response = await fetch('/api/v1/platform-config/monitoring', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({ base_url: config.base_url, request_timeout: config.request_timeout }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setConfig({ ...emptyMonitoringConfig, ...payload.data });
      setMessage(tx('监控数据源已保存并即时生效', 'Monitoring data source saved and applied immediately'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const test = async () => {
    setTesting(true); setMessage(''); setError('');
    if (!adminToken.trim()) { setTesting(false); setError(tx('请先填写管理口令', 'Enter the management token first')); return; }
    try {
      const response = await fetch('/api/v1/platform-config/monitoring/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({ base_url: config.base_url, request_timeout: config.request_timeout }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setMessage(tx(`连接正常：${payload.data.targets} 个 Target，${payload.data.up} 个 UP`, `Connection OK: ${payload.data.targets} targets, ${payload.data.up} up`));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('连接测试失败', 'Connection test failed'));
    } finally { setTesting(false); }
  };
  const valid = loaded && config.base_url.trim() !== '';
  return <>
    <Card className="span-8 lxop-settings">
      <CardHead code="MONITORING SOURCE" title={tx('监控数据源', 'Monitoring Data Source')} action={<Badge value={config.configured ? 'CONFIGURED' : 'UNSET'} kind={config.configured ? 'healthy' : 'warning'} />} />
      <p className="settings-intro">{tx('Prometheus / VictoriaMetrics 查询地址，保存即生效', 'Prometheus / VictoriaMetrics query URL; applies on save')}</p>
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('监控地址', 'Monitoring base URL')}<small>{tx('例如 VictoriaMetrics 集群 select 地址', 'e.g. VictoriaMetrics cluster select endpoint')}</small></span><input value={config.base_url} onChange={event => setConfig(current => ({ ...current, base_url: event.target.value }))} placeholder="http://10.80.192.233:28481/select/0/prometheus" /></label>
        <label><span>{tx('请求超时', 'Request timeout')}<small>{tx('Go 时长，如 15s', 'Go duration, e.g. 15s')}</small></span><input value={config.request_timeout} onChange={event => setConfig(current => ({ ...current, request_timeout: event.target.value }))} placeholder="15s" /></label>
        <label><span>{tx('管理口令', 'Management token')}<small>X-Atlas-Admin-Token</small></span><input type="password" autoComplete="off" value={adminToken} onChange={event => setAdminToken(event.target.value)} /></label>
        {error && <p className="form-error">{error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('保存并生效', 'Save & Apply')}</button>
        <button className="primary-action" type="button" disabled={testing || !valid} onClick={() => void test()}><RefreshCw size={15} />{testing ? tx('测试中', 'Testing') : tx('测试连接', 'Test Connection')}</button>
      </form>
    </Card>
    <Card className="span-4 settings-preview lxop-status">
      <CardHead code="EFFECTIVE SOURCE" title={tx('当前生效地址', 'Effective Address')} />
      <div className="preview-environment"><i /><span><b>{config.effective_url || config.base_url || '—'}</b><small>{config.configured ? tx('已保存到数据库', 'Persisted in database') : tx('尚未配置', 'Not configured yet')}</small></span></div>
      <dl>
        <div><dt>{tx('超时', 'Timeout')}</dt><dd>{config.request_timeout || '15s'}</dd></div>
        <div><dt>{tx('生效方式', 'Apply mode')}</dt><dd>{tx('保存即生效 · 无需重启', 'Immediate · no restart')}</dd></div>
      </dl>
      {config.updated_at && <small className="settings-updated">{tx('最近更新', 'Last updated')} · {time(config.updated_at)}</small>}
    </Card>
  </>;
}
function AssetAuditCheck({ tx, lang }: { tx: Tx; lang: string }) {
  const [adminToken, setAdminToken] = useAdminToken();
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [runs, setRuns] = useState<AssetAuditRun[]>([]);
  const [marks, setMarks] = useState<AssetAuditMark[]>([]);
  const [markBusy, setMarkBusy] = useState(false);
  const [selectedIPs, setSelectedIPs] = useState<Set<string>>(() => new Set());
  const [deliverySelection, setDeliverySelection] = useState('missing_asset');
  const [deliveryPage, setDeliveryPage] = useState(0);
  const [scanSelection, setScanSelection] = useState('unregistered');
  const [scanPage, setScanPage] = useState(0);
  const poll = useRef<number | null>(null);
  const loadRuns = useCallback(async () => {
    try {
      const response = await fetch('/api/v1/asset-audit/runs?limit=40');
      if (!response.ok) return;
      const payload = await response.json();
      const rows: AssetAuditRun[] = Array.isArray(payload.data) ? payload.data : [];
      setRuns(rows);
      if (!rows.some(row => row.status === 'running') && poll.current != null) {
        window.clearInterval(poll.current); poll.current = null;
      }
    } catch { /* transient network error; keep the last result */ }
  }, []);
  const loadMarks = useCallback(async () => {
    try {
      const response = await fetch('/api/v1/asset-audit/marks');
      if (!response.ok) return;
      const payload = await response.json();
      setMarks(Array.isArray(payload.data) ? payload.data : []);
    } catch { /* transient network error; keep the last result */ }
  }, []);
  useEffect(() => {
    void loadRuns();
    void loadMarks();
    return () => { if (poll.current != null) { window.clearInterval(poll.current); poll.current = null; } };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    if (runs.some(row => row.status === 'running') && poll.current == null) {
      poll.current = window.setInterval(() => void loadRuns(), 4000);
    }
  }, [runs, loadRuns]);
  // Selection is scoped to the active category so a bulk action never touches
  // rows the operator is no longer looking at.
  useEffect(() => { setSelectedIPs(new Set()); }, [scanSelection]);
  const submitMarks = async (method: 'POST' | 'DELETE', addresses: string[], note: string) => {
    const targets = [...new Set(addresses.filter(address => address.trim() !== ''))];
    if (targets.length === 0) return;
    if (!adminToken.trim()) { setError(tx('请先填写管理口令', 'Enter the management token first')); return; }
    setMessage(''); setError(''); setMarkBusy(true);
    try {
      const response = await fetch('/api/v1/asset-audit/marks', {
        method,
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({ ip_addresses: targets, reason: note }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      const count = payload.data?.marked ?? payload.data?.unmarked ?? targets.length;
      setMessage(method === 'POST' ? tx(`已标记 ${count} 个 IP`, `Marked ${count} IPs`) : tx(`已取消标记 ${count} 个 IP`, `Unmarked ${count} IPs`));
      setSelectedIPs(current => { const next = new Set(current); targets.forEach(address => next.delete(address)); return next; });
      await loadMarks();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('标记操作失败', 'Mark operation failed'));
    } finally { setMarkBusy(false); }
  };
  const issueLabel = (issue: string) => issue === 'missing_asset' ? tx('未录入资产', 'Missing asset') : issue === 'ip_mismatch' ? tx('IP 不一致', 'IP mismatch') : issue === 'sn_notice' ? tx('SN 提示', 'SN notice') : issue;
  const scanStatusLabel = (status: string) => status === 'unregistered' ? tx('存活未登记', 'Unregistered') : status === 'marked' ? tx('已标记', 'Marked') : status === 'port22_closed' ? tx('22 端口不通', 'Port 22 closed') : status === 'ssh_login_failed' ? tx('SSH 登录失败', 'SSH login failed') : status;
  const auditPageSize = 10;
  const auditSlice = <T,>(rows: T[], page: number) => rows.slice(page * auditPageSize, (page + 1) * auditPageSize);
  const auditPager = (total: number, page: number, setPage: (next: number) => void) => {
    const pages = Math.max(1, Math.ceil(total / auditPageSize));
    return <div className="table-actions asset-audit-pager"><span>{total} {tx('条', 'items')}</span><button type="button" aria-label={tx('上一页', 'Previous page')} disabled={page === 0} onClick={() => setPage(Math.max(0, page - 1))}><ChevronLeft size={15} /></button><span>{page + 1} / {pages}</span><button type="button" aria-label={tx('下一页', 'Next page')} disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}><ChevronRight size={15} /></button></div>;
  };
  const metricCard = (key: string, label: string, value: number, icon: ReactNode) => <Card className="metric" key={key}>{icon}<span>{label}</span><strong>{value}</strong></Card>;
  const metricButton = (key: string, label: string, value: number, active: boolean, onSelect: () => void, icon: ReactNode) => <button type="button" key={key} className={`card metric metric-selectable${active ? ' active' : ''}`} onClick={onSelect}>{icon}<span>{label}</span><strong>{value}</strong></button>;
  const latestDelivery = runs.find(run => run.kind === 'delivery_check');
  const latestScan = runs.find(run => run.kind === 'ip_scan');
  const deliverySummary = (latestDelivery?.summary || null) as AssetAuditDeliverySummary | null;
  const deliveryFindings = (latestDelivery?.findings || []) as AssetAuditDeliveryFinding[];
  const deliveryExcluded = (deliverySummary?.excluded || []) as AssetAuditDeliveryExclusion[];
  const deliveryRows = (rows: AssetAuditDeliveryFinding[]) => <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th>{tx('问题', 'Issue')}</th><th>{tx('节点', 'Node')}</th><th>{tx('交付表 IP / SN', 'Sheet IP / SN')}</th><th>{tx('资产 IP / SN', 'Asset IP / SN')}</th><th>{tx('实测 SN', 'Telemetry SN')}</th><th>{tx('交付时间', 'Delivered')}</th></tr></thead><tbody>{rows.map(finding => <tr key={`${finding.row}-${finding.issue}`}><td><Badge value={issueLabel(finding.issue)} kind={finding.issue === 'missing_asset' ? 'danger' : 'warning'} /><small>{finding.detail}</small></td><td><b>{finding.hostname || finding.ip_address || '—'}</b><small>{tx('第', 'row')} {finding.row} {tx('行', '')}</small></td><td><code>{finding.ip_address || '—'}</code><small><code>{finding.sn || 'NO SN'}</code></small></td><td><code>{finding.asset_ip || '—'}</code><small><code>{finding.asset_sn || 'NO SN'}</code></small></td><td><code>{finding.telemetry_sn || '—'}</code></td><td>{finding.delivered_at || '—'}</td></tr>)}</tbody></table></div>;
  const deliveryExcludedRows = (rows: AssetAuditDeliveryExclusion[]) => <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th>{tx('行', 'Row')}</th><th>{tx('节点', 'Node')}</th><th>{tx('交付表 IP', 'Sheet IP')}</th><th>{tx('排除原因', 'Reason')}</th></tr></thead><tbody>{rows.map(item => <tr key={`${item.row}-${item.reason}`}><td>{tx('第', 'row')} {item.row} {tx('行', '')}</td><td><b>{item.hostname || '—'}</b></td><td><code>{item.ip_address || '—'}</code></td><td><small>{item.reason}</small></td></tr>)}</tbody></table></div>;
  const deliveryDetail = () => {
    if (deliverySelection === 'excluded') {
      if (deliveryExcluded.length === 0) return <Empty tx={tx} title={tx('无被排除的交付节点', 'No excluded delivered nodes')} />;
      return <>{deliveryExcludedRows(auditSlice(deliveryExcluded, deliveryPage))}{auditPager(deliveryExcluded.length, deliveryPage, setDeliveryPage)}</>;
    }
    const rows = deliveryFindings.filter(finding => finding.issue === deliverySelection);
    if (rows.length === 0) {
      const clean = (deliverySummary?.missing_asset ?? 0) === 0 && (deliverySummary?.ip_mismatch ?? 0) === 0;
      return <Empty tx={tx} title={deliverySelection === 'missing_asset' && clean ? tx('全部已交付节点与资产一致', 'All delivered nodes match the asset registry') : tx('无此类记录', 'No records in this category')} />;
    }
    return <>{deliveryRows(auditSlice(rows, deliveryPage))}{auditPager(rows.length, deliveryPage, setDeliveryPage)}</>;
  };
  const scanSummary = (latestScan?.summary || null) as AssetAuditScanSummary | null;
  const scanFindings = (latestScan?.findings || []) as AssetAuditScanFinding[];
  const scanHosts = (scanSummary?.hosts || []) as AssetAuditScanHost[];
  const scanUnregisteredHosts: AssetAuditScanHost[] = scanFindings.map(finding => ({ ip_address: finding.ip_address, subnet: finding.subnet, status: 'unregistered' }));
  const markedSet = useMemo(() => new Set(marks.map(mark => mark.ip_address)), [marks]);
  const isMarked = (address: string, status?: string) => status === 'marked' || markedSet.has(address);
  const toggleSelected = (address: string) => setSelectedIPs(current => { const next = new Set(current); if (next.has(address)) next.delete(address); else next.add(address); return next; });
  const toggleAll = (addresses: string[], checked: boolean) => setSelectedIPs(current => { const next = new Set(current); addresses.forEach(address => checked ? next.add(address) : next.delete(address)); return next; });
  const selectedList = [...selectedIPs];
  const selectedUnmarked = selectedList.filter(address => !markedSet.has(address));
  const selectedMarked = selectedList.filter(address => markedSet.has(address));
  const scanHostRows = (rows: AssetAuditScanHost[]) => {
    const addresses = rows.map(host => host.ip_address);
    const allSelected = addresses.length > 0 && addresses.every(address => selectedIPs.has(address));
    return <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th className="mark-select"><input type="checkbox" aria-label={tx('全选', 'Select all')} checked={allSelected} disabled={addresses.length === 0} onChange={event => toggleAll(addresses, event.target.checked)} /></th><th>IP</th><th>{tx('网段', 'Subnet')}</th><th>{tx('状态', 'Status')}</th><th>{tx('已标记', 'Marked')}</th></tr></thead><tbody>{rows.map(host => <tr key={host.ip_address}><td className="mark-select"><input type="checkbox" aria-label={`${tx('选择', 'Select')} ${host.ip_address}`} checked={selectedIPs.has(host.ip_address)} onChange={() => toggleSelected(host.ip_address)} /></td><td><code>{host.ip_address}</code></td><td>{host.subnet}</td><td><small>{scanStatusLabel(host.status)}</small></td><td>{isMarked(host.ip_address, host.status) ? <Badge value={tx('已标记', 'MARKED')} kind="info" /> : <span className="muted">—</span>}</td></tr>)}</tbody></table></div>;
  };
  const markToolbar = () => <div className="table-actions audit-mark-toolbar">
    <span className="audit-mark-count">{tx(`已选 ${selectedList.length} 项`, `${selectedList.length} selected`)}</span>
    <button className="primary-action" type="button" disabled={markBusy || selectedUnmarked.length === 0} onClick={() => void submitMarks('POST', selectedUnmarked, tx('手动标记', 'Manual mark'))}><CheckCircle2 size={15} />{tx(`标记所选（${selectedUnmarked.length}）`, `Mark selected (${selectedUnmarked.length})`)}</button>
    <button className="primary-action" type="button" disabled={markBusy || selectedMarked.length === 0} onClick={() => void submitMarks('DELETE', selectedMarked, '')}>{tx(`取消标记所选（${selectedMarked.length}）`, `Unmark selected (${selectedMarked.length})`)}</button>
    <button className="link-action" type="button" disabled={markBusy || selectedList.length === 0} onClick={() => setSelectedIPs(new Set())}>{tx('清除选择', 'Clear selection')}</button>
    {!adminToken.trim() && <input className="audit-mark-token" type="password" autoComplete="off" value={adminToken} onChange={event => setAdminToken(event.target.value)} placeholder={tx('管理口令（标记需要）', 'Management token (required to mark)')} />}
  </div>;
  const scanSubnetRows = (rows: AssetAuditScanSubnet[]) => <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th>{tx('网段', 'Subnet')}</th><th>{tx('扫描地址数', 'Scanned addresses')}</th><th>{tx('存活未登记', 'Unregistered live')}</th></tr></thead><tbody>{rows.map(item => <tr key={item.cidr}><td><code>{item.cidr}</code></td><td>{item.hosts}</td><td>{item.live}</td></tr>)}</tbody></table></div>;
  const scanDetail = () => {
    if (scanSelection === 'subnets') {
      const rows = scanSummary?.subnets || [];
      if (rows.length === 0) return <Empty tx={tx} title={tx('无扫描网段', 'No scanned subnets')} />;
      return <>{scanSubnetRows(auditSlice(rows, scanPage))}{auditPager(rows.length, scanPage, setScanPage)}</>;
    }
    const rows = scanSelection === 'unregistered' ? scanUnregisteredHosts
      : scanSelection === 'marked' ? scanHosts.filter(host => host.status === 'marked')
        : scanSelection === 'icmp' ? scanHosts
          : scanSelection === 'port22' ? scanHosts.filter(host => host.status === 'port22_closed')
            : scanHosts.filter(host => host.status === 'ssh_login_failed');
    if (rows.length === 0) return <Empty tx={tx} title={scanSelection === 'unregistered' ? tx('未发现未录入资产的存活 IP', 'No unregistered live IPs found') : tx('无此类记录', 'No records in this category')} />;
    return <>{scanHostRows(auditSlice(rows, scanPage))}{auditPager(rows.length, scanPage, setScanPage)}</>;
  };
  const runTone = (status: string) => status === 'success' ? 'healthy' : status === 'running' ? 'info' : status === 'failed' ? 'warning' : 'neutral';
  const runBadge = (run?: AssetAuditRun) => run ? <Badge value={`${run.status.toUpperCase()} · ${run.trigger.toUpperCase()}`} kind={runTone(run.status)} /> : <Badge value="NO RUN" kind="neutral" />;
  return <div className="grid asset-audit">
    <Card className="span-12">
      <CardHead code="DELIVERY CHECK" title={tx('最近一次已交付节点核查', 'Latest Delivered-Node Check')} action={runBadge(latestDelivery)} />
      {!latestDelivery && <Empty tx={tx} title={tx('尚无核查记录', 'No check run yet')} />}
      {latestDelivery && <>
        {latestDelivery.error_message && <p className="form-error">{latestDelivery.error_message}</p>}
        {latestDelivery.notify_error && <p className="form-error">{tx('通知失败：', 'Notification failed: ')}{latestDelivery.notify_error}</p>}
        {deliverySummary && <>
          <p className="asset-audit-meta">{tx('核查时间', 'Checked')} {time(deliverySummary.generated_at, lang)}</p>
          <div className="health-kpi-grid">
            {metricCard('sheet_rows', tx('交付表行数', 'Delivery sheet rows'), deliverySummary.sheet_rows, <ClipboardList size={18} />)}
            {metricCard('delivered_rows', tx('已交付节点', 'Delivered'), deliverySummary.delivered_rows, <CheckCircle2 size={18} />)}
            {metricButton('excluded_rows', tx('已排除', 'Excluded'), deliverySummary.excluded_rows, deliverySelection === 'excluded', () => { setDeliverySelection('excluded'); setDeliveryPage(0); }, <AlertTriangle size={18} />)}
            {metricCard('matched', tx('一致', 'Matched'), deliverySummary.matched, <CheckCircle2 size={18} />)}
            {metricButton('missing_asset', tx('未录入资产', 'Missing asset'), deliverySummary.missing_asset, deliverySelection === 'missing_asset', () => { setDeliverySelection('missing_asset'); setDeliveryPage(0); }, <AlertTriangle size={18} />)}
            {metricButton('ip_mismatch', tx('IP 不一致', 'IP mismatch'), deliverySummary.ip_mismatch, deliverySelection === 'ip_mismatch', () => { setDeliverySelection('ip_mismatch'); setDeliveryPage(0); }, <AlertTriangle size={18} />)}
            {metricButton('sn_notice', tx('SN 提示', 'SN notice'), deliverySummary.sn_notice, deliverySelection === 'sn_notice', () => { setDeliverySelection('sn_notice'); setDeliveryPage(0); }, <AlertTriangle size={18} />)}
          </div>
          <div className="asset-audit-detail">{deliveryDetail()}</div>
        </>}
      </>}
    </Card>
    <Card className="span-12">
      <CardHead code="LIVE-IP SCAN" title={tx('最近一次存活 IP 扫描', 'Latest Live-IP Scan')} action={runBadge(latestScan)} />
      {!latestScan && <Empty tx={tx} title={tx('尚无扫描记录', 'No scan run yet')} />}
      {latestScan && <>
        {latestScan.error_message && <p className="form-error">{latestScan.error_message}</p>}
        {latestScan.notify_error && <p className="form-error">{tx('通知失败：', 'Notification failed: ')}{latestScan.notify_error}</p>}
        {scanSummary && <>
          <p className="asset-audit-meta">{tx('扫描时间', 'Scanned')} {time(scanSummary.generated_at, lang)}</p>
          <div className="health-kpi-grid">
            {metricButton('subnets', tx('扫描网段', 'Subnets'), scanSummary.subnet_count, scanSelection === 'subnets', () => { setScanSelection('subnets'); setScanPage(0); }, <Network size={18} />)}
            {metricCard('known_asset_ips', tx('已登记资产 IP', 'Known asset IPs'), scanSummary.known_asset_ips, <Network size={18} />)}
            {metricCard('delivery_sheet_ips', tx('交付表节点 IP', 'Delivery sheet IPs'), scanSummary.delivery_sheet_ips ?? 0, <Network size={18} />)}
            {metricCard('scanned_targets', tx('扫描地址数', 'Scanned addresses'), scanSummary.scanned_targets, <Network size={18} />)}
            {metricButton('icmp', tx('ICMP 存活', 'ICMP alive'), scanSummary.live_count ?? 0, scanSelection === 'icmp', () => { setScanSelection('icmp'); setScanPage(0); }, <Network size={18} />)}
            {metricButton('port22', tx('22 端口不通', 'Port 22 closed'), scanSummary.port22_closed ?? 0, scanSelection === 'port22', () => { setScanSelection('port22'); setScanPage(0); }, <AlertTriangle size={18} />)}
            {metricButton('ssh_failed', tx('SSH 登录失败', 'SSH login failed'), scanSummary.ssh_login_failed ?? 0, scanSelection === 'ssh_failed', () => { setScanSelection('ssh_failed'); setScanPage(0); }, <AlertTriangle size={18} />)}
            {metricButton('marked', tx('已标记（不计数）', 'Marked (excluded)'), scanSummary.marked ?? 0, scanSelection === 'marked', () => { setScanSelection('marked'); setScanPage(0); }, <CheckCircle2 size={18} />)}
            {metricButton('unregistered', tx('存活未登记 IP', 'Unregistered live IPs'), scanSummary.unregistered, scanSelection === 'unregistered', () => { setScanSelection('unregistered'); setScanPage(0); }, <AlertTriangle size={18} />)}
          </div>
          {scanSummary.truncated && <p className="form-error">{tx('本次扫描达到地址上限，结果可能不完整。', 'This scan hit the address cap; results may be incomplete.')}</p>}
          {error && <p className="form-error">{error}</p>}
          {message && <p className="form-success">{message}</p>}
          {markToolbar()}
          <div className="asset-audit-detail">{scanDetail()}</div>
        </>}
      </>}
    </Card>
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

function HardwareEvents({ tx, faultRows, faultMeta, faultPage, faultLoading, previousFaultPage, nextFaultPage, query, setQuery, level, setLevel, selectIssue, selectReport, lang }: { tx: Tx; faultRows: FaultEvent[]; faultMeta: CursorMeta; faultPage: number; faultLoading: boolean; previousFaultPage: () => void; nextFaultPage: () => void; query: string; setQuery: (v: string) => void; level: string; setLevel: (v: string) => void; selectIssue: (id: number) => void; selectReport: (id: number) => void; lang: string }) {
  const faultStart = faultMeta.total > 0 ? (faultPage - 1) * faultMeta.limit + 1 : 0;
  const faultEnd = faultStart > 0 ? faultStart + faultRows.length - 1 : 0;
  return <div className="grid"><Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('节点 / UUID / 规则 / 详情', 'Node / UUID / rule / details')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'warning', 'attention'].map(x => <option key={x}>{x}</option>)}</select></label><span>{faultLoading ? tx('加载中', 'Loading') : `${faultStart}–${faultEnd} / ${faultMeta.total}`}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('规则 / 详情', 'Rule / Details')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('维度', 'Domain')}</th><th>{tx('告警状态', 'Alert')}</th><th>{tx('处置状态', 'Workflow')}</th><th>{tx('次数', 'Count')}</th><th>{tx('首次 / 最近', 'First / Last')}</th><th /></tr></thead><tbody>{faultRows.map(x => <tr key={x.id}><td><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /></td><td><b>{x.rule_code}</b><small>{x.evidence} · {tx('阈值', 'threshold')} {x.threshold}</small></td><td><b>{x.node_ip} · GPU {x.gpu_index}</b><small><code>{x.gpu_uuid}</code></small></td><td><code>{x.domain.toUpperCase()}</code></td><td><Badge value={x.state.toUpperCase()} kind={tone(x.state)} /></td><td><Badge value={(x.workflow_status || 'syncing').toUpperCase()} kind={x.workflow_status ? issueTone(x.workflow_status) : 'neutral'} /></td><td>{x.occurrence_count}</td><td>{time(x.first_observed_at, lang)}<small>{time(x.last_observed_at, lang)}</small></td><td><div className="event-actions"><button className="link" onClick={() => selectReport(x.id)}>{tx('分析报告', 'Report')}<BookOpen size={13} /></button>{x.issue_id ? <button className="link" onClick={() => selectIssue(x.issue_id!)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button> : <small>{tx('台账同步中', 'Syncing')}</small>}</div></td></tr>)}</tbody></table>{faultRows.length === 0 && <Empty tx={tx} title={tx('无匹配硬件告警', 'No matching hardware alerts')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${faultPage} 页`, `Page ${faultPage}`)}</span><div><button onClick={previousFaultPage} disabled={faultPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextFaultPage} disabled={!faultMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card></div>;
}

function Validations({ tx, summary, candidates, lang }: { tx: Tx; summary: DegradationSummary | null; candidates: DegradationCandidate[]; lang: string }) {
  const scope = (value: string) => value === 'same_model_high_load_7d' ? tx('同型号高负载 7 天', 'Same-model high-load 7d') : value === 'same_node_model' ? tx('同节点同型号', 'Same node/model') : tx('同型号集群', 'Same-model fleet');
  return <div className="grid">
    <Card className="span-12 degradation-guard"><div><Eye size={21} /><div><CardHead code="SHADOW MODE" title={tx('被动衰减候选', 'Passive Degradation Candidates')} /><p>{tx('影子模式 · 不影响健康分 · 维护窗口复测确认', 'Shadow mode · no health impact · confirm in maintenance window')}</p></div></div><Badge value={(summary?.mode || 'shadow').toUpperCase()} kind="info" /></Card>
    <Card className="span-3 degradation-stat"><small>{tx('已评估', 'EVALUATED')}</small><strong>{summary?.evaluated_gpus ?? '—'}</strong><span>{tx('当前健康快照', 'current health snapshots')}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('高负载可评估', 'LOAD ELIGIBLE')}</small><strong>{summary?.eligible_gpus ?? '—'}</strong><span>{tx(`利用率 ≥ ${summary?.minimum_utilization ?? 80}%`, `utilization ≥ ${summary?.minimum_utilization ?? 80}%`)}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('基线就绪', 'BASELINE READY')}</small><strong>{summary?.baseline_ready_gpus ?? '—'}</strong><span>{tx(`历史基线 ${summary?.historical_baseline_gpus ?? 0}`, `${summary?.historical_baseline_gpus ?? 0} historical`)}</span></Card>
    <Card className="span-3 degradation-stat candidate"><small>{tx('衰减候选', 'CANDIDATES')}</small><strong>{summary?.candidate_gpus ?? '—'}</strong><span>{tx(`性能比 < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`, `performance ratio < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`)}</span></Card>
    <Card className="span-12 degradation-table-card"><CardHead code={summary?.version || 'degradation-v0.2.0'} title={tx('候选明细', 'Candidate Details')} action={<span className="table-observed">{tx('最近观测', 'LATEST OBSERVED')} {time(summary?.latest_observed_at, lang)}</span>} />
      {candidates.length > 0 ? <div className="table-wrap"><table><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('负载', 'Load')}</th><th>{tx('时钟观测 / 基线', 'Clock observed / baseline')}</th><th>{tx('性能比', 'Ratio')}</th><th>{tx('基线', 'Baseline')}</th><th>{tx('置信度', 'Confidence')}</th><th>{tx('建议', 'Recommendation')}</th></tr></thead><tbody>{candidates.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip}</b><small>GPU {item.gpu_index} · {item.gpu_uuid || 'UUID UNKNOWN'}</small></td><td>{item.model_name || 'UNKNOWN'}</td><td>{item.gpu_utilization.toFixed(1)}%</td><td><b>{item.observed_value.toFixed(0)} MHz</b><small>{item.baseline_value.toFixed(0)} MHz</small></td><td><Badge value={`${(item.performance_ratio * 100).toFixed(1)}%`} kind="warning" /></td><td><b>{scope(item.baseline_scope)}</b><small>{item.peer_count} {tx('个对照 GPU', 'peer GPUs')}</small></td><td><Badge value={item.data_confidence} kind={item.data_confidence === 'A' ? 'healthy' : 'info'} /></td><td><span className="recommendation">{tx('维护窗口 DCGM / SuperBench 复测', 'Retest with DCGM / SuperBench in maintenance window')}</span><small>{tx('源时间', 'source time')} {time(item.observed_at, lang)}</small></td></tr>)}</tbody></table></div> : <Empty tx={tx} title={tx('当前没有被动衰减候选', 'No passive degradation candidates')} />}
    </Card>
  </div>;
}
function Quality({ tx, view, targets, summary, inventoryError, syncRuns, assetChanges, telemetry, telemetrySummary, lang }: { tx: Tx; view: string; targets: CollectorTarget[]; summary: FleetSummary | null; inventoryError: boolean; syncRuns: SyncRun[]; assetChanges: AssetChange[]; telemetry: TelemetryQualityItem[]; telemetrySummary: TelemetryQualitySummary | null; lang: string }) {
  const [coverageJob, setCoverageJob] = useState('node_exporter');
  const [expandedRunID, setExpandedRunID] = useState<number | null>(null);
  const definitions = [['dcgm_exporter', 'DCGM Exporter', 'GPU Telemetry'], ['gpu_exporter', 'GPU Exporter', 'NVML Extension'], ['node_exporter', 'Node Exporter', 'Host OS'], ['ipmi_exporter', 'IPMI Exporter', 'BMC / Hardware']] as const;
  if (view === 'continuity') {
    const problems = telemetry.filter(item => item.status !== 'fresh');
    return <div className="grid">
      <Card className="span-12 capability-note"><Activity size={19} /><div><CardHead code={telemetrySummary?.feature_catalog_version || 'FEATURE CATALOG'} title={tx('GPU 指标连续性', 'GPU Metric Continuity')} /><p>{tx('15 s 采样基线 · 每卡 1 h 存在率 / 最大间隔 / UUID 波动', '15 s sampling baseline · per-GPU 1 h presence / max gap / UUID churn')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>
      {[[tx('GPU 总数', 'Total GPUs'), telemetrySummary?.total ?? '—', 'LATEST RUN'], [tx('连续正常', 'Fresh'), telemetrySummary?.by_status.fresh ?? 0, '≥95% · AGE≤60s'], [tx('连续性异常', 'Degraded / Stale'), (telemetrySummary?.by_status.degraded || 0) + (telemetrySummary?.by_status.stale || 0), '<95% OR AGE>60s'], [tx('平均存在率', 'Average Presence'), telemetrySummary ? `${telemetrySummary.average_presence_ratio_1h.toFixed(2)}%` : '—', `MAX GAP ${telemetrySummary?.max_metric_gap_seconds_1h?.toFixed(1) || '—'}s · FLAP ${telemetrySummary?.max_uuid_flap_count_1h?.toFixed(0) || '0'}`]].map(([label, value, note]) => <Card className="metric quality compact-quality-metric" key={String(label)}><Activity size={17} /><strong>{String(value)}</strong><b>{String(label)}</b><small>{String(note)}</small></Card>)}
      <Card className="span-12"><CardHead code="CONTINUITY ISSUES" title={tx('异常与未知 GPU', 'Degraded and Unknown GPUs')} action={<div className="table-actions"><Badge value={`SCRAPE ${telemetrySummary?.min_target_scrape_success_ratio_5m?.toFixed(1) || '—'}%`} kind={(telemetrySummary?.min_target_scrape_success_ratio_5m ?? 100) < 95 ? 'warning' : 'healthy'} /><Badge value={`${problems.length} ITEMS`} kind={problems.length ? 'warning' : 'healthy'} /></div>} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>UUID</th><th>{tx('状态', 'Status')}</th><th>{tx('1h 样本 / 存在率', '1h Samples / Presence')}</th><th>{tx('年龄 / 最大间隔', 'Age / Max Gap')}</th><th>{tx('UUID 波动', 'UUID Flaps')}</th><th>{tx('Target 抓取', 'Target Scrape')}</th><th>{tx('观测时间', 'Observed')}</th></tr></thead><tbody>{problems.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip} · GPU {item.gpu_index}</b><small>{item.model_name}</small></td><td><code>{item.gpu_uuid}</code></td><td><Badge value={item.status.toUpperCase()} kind={item.status === 'stale' ? 'danger' : 'warning'} /></td><td>{item.sample_count_1h?.toFixed(0) ?? '—'} / 240<small>{item.presence_ratio_1h != null ? `${item.presence_ratio_1h.toFixed(1)}%` : '—'}</small></td><td>{item.sample_age_seconds != null ? `${item.sample_age_seconds.toFixed(1)}s` : '—'}<small>{tx('最大', 'max')} {item.metric_gap_max_seconds_1h != null ? `${item.metric_gap_max_seconds_1h.toFixed(1)}s` : '—'}</small></td><td>{item.uuid_presence_flap_count_1h?.toFixed(0) ?? '—'}</td><td>{item.target_scrape_success_ratio_5m != null ? `${item.target_scrape_success_ratio_5m.toFixed(1)}%` : '—'}<small>{tx('样本', 'samples')} {item.target_scrape_samples_ratio_5m?.toFixed(1) ?? '—'}% · {tx('耗时比', 'duration')} {item.target_scrape_duration_ratio_5m?.toFixed(1) ?? '—'}%</small></td><td>{time(item.observed_at, lang)}</td></tr>)}</tbody></table>{problems.length === 0 && <Empty tx={tx} title={tx('GPU 指标连续性正常', 'GPU metric continuity is healthy')} />}</div></Card>
    </div>;
  }
  if (view === 'audit') return <div className="grid">
    <Card className="span-12"><CardHead code="SYNC RUNS" title={tx('同步批次', 'Reconciliation Runs')} action={<Badge value={`${syncRuns.length} RUNS`} kind="info" />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('任务', 'Task')}</th><th>{tx('状态', 'Status')}</th><th>{tx('节点 / GPU / UUID', 'Nodes / GPUs / UUIDs')}</th><th>TARGETS</th><th>{tx('变化', 'Changes')}</th><th>{tx('开始时间', 'Started')}</th><th>{tx('耗时', 'Duration')}</th></tr></thead><tbody>{syncRuns.map(run => { const failed = run.status === 'failed'; const expanded = failed && expandedRunID === run.id; return <Fragment key={run.id}><tr className={failed ? 'audit-run-row clickable' : undefined} aria-expanded={failed ? expanded : undefined} onClick={failed ? () => setExpandedRunID(current => (current === run.id ? null : run.id)) : undefined}><td><code>#{run.id}</code></td><td><b>{run.task_type || 'legacy'}</b></td><td><Badge value={run.status.toUpperCase()} kind={run.status === 'success' ? 'healthy' : failed ? 'danger' : 'warning'} />{failed && <ChevronRight size={13} className={expanded ? 'audit-row-toggle open' : 'audit-row-toggle'} />}</td><td>{run.node_count} / {run.gpu_count} / {run.known_uuid_count}</td><td>{run.target_count}</td><td>{run.change_count}</td><td>{time(run.started_at, lang)}</td><td>{run.finished_at ? `${Math.max(0, (new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1)}s` : '—'}</td></tr>{expanded && <tr className="audit-error-row"><td colSpan={8}><AlertTriangle size={14} />{run.error_message || tx('未记录失败原因', 'No failure reason recorded')}</td></tr>}</Fragment>; })}</tbody></table>{syncRuns.length === 0 && <Empty tx={tx} title={tx('暂无同步批次', 'No sync runs')} />}</div></Card>
    <Card className="span-12"><CardHead code="CHANGE LOG" title={tx('资产变更', 'Asset Changes')} action={<Badge value={`${assetChanges.length} EVENTS`} kind={assetChanges.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('事件', 'Event')}</th><th>{tx('节点', 'Node')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('原值', 'Before')}</th><th>{tx('新值', 'After')}</th><th>RUN</th><th>{tx('时间', 'Time')}</th></tr></thead><tbody>{assetChanges.map(change => <tr key={change.id}><td><code>#{change.id}</code></td><td><Badge value={change.event_type.toUpperCase()} kind={change.event_type.includes('retired') ? 'danger' : 'warning'} /></td><td><b>{change.node_ip}</b></td><td><code>{change.asset_key}</code></td><td><code>{change.old_value || '—'}</code></td><td><code>{change.new_value || '—'}</code></td><td>#{change.sync_run_id || '—'}</td><td>{time(change.created_at, lang)}</td></tr>)}</tbody></table>{assetChanges.length === 0 && <Empty tx={tx} title={tx('暂无资产变化', 'No asset changes')} />}</div></Card>
  </div>;
  type CoverageRow = { node_ip: string; target_ip?: string; job: string; health: string; reason_code?: string; suppressed?: boolean; suppression_reason?: string; last_error?: string; last_synced_at?: string };
  const coverageByNodeJob = new Map<string, CollectorTarget>();
  for (const target of targets) coverageByNodeJob.set(`${target.node_ip}|${target.job}`, target);
  const nodeExporterUp = new Set(targets.filter(target => target.job === 'node_exporter' && target.health === 'up').map(target => target.node_ip));
  const coverageRows: CoverageRow[] = [];
  if (coverageJob === 'node_exporter') {
    for (const target of targets) {
      if (target.job !== 'node_exporter' || target.health === 'up') continue;
      coverageRows.push({ node_ip: target.node_ip, target_ip: target.target_ip, job: target.job, health: target.health, reason_code: target.reason_code, suppressed: target.suppressed, suppression_reason: target.suppression_reason, last_error: target.last_error, last_synced_at: target.last_synced_at });
    }
  } else {
    for (const nodeIP of nodeExporterUp) {
      const target = coverageByNodeJob.get(`${nodeIP}|${coverageJob}`);
      if (target?.health === 'up') continue;
      coverageRows.push({ node_ip: nodeIP, target_ip: target?.target_ip, job: coverageJob, health: target?.health || 'missing', reason_code: target?.reason_code, suppressed: target?.suppressed, suppression_reason: target?.suppression_reason, last_error: target?.last_error, last_synced_at: target?.last_synced_at });
    }
  }
  const ipmiReasonHint = (code?: string) => ({
    ipmi_collector_all_failed: tx('bmc / chassis / ipmi 全部失败：账号密码被拒绝或 BMC 不可达（指标无法区分，需看 exporter 日志）', 'bmc / chassis / ipmi all failed: login rejected or BMC unreachable (metrics cannot tell which; check exporter log)'),
    ipmi_collector_username_invalid: tx('BMC 用户名无效：该 BMC 上没有 exporter 配置的账号', 'BMC username invalid: the configured account does not exist on this BMC'),
    ipmi_collector_password_invalid: tx('BMC 密码无效：账号存在但密码被拒绝', 'BMC password invalid: the account exists but the password is rejected'),
    ipmi_collector_connection_timeout: tx('BMC 连接超时：网络不可达或 BMC 无响应', 'BMC connection timeout: network unreachable or BMC not responding'),
    ipmi_collector_sdr_cache_error: tx('BMC 传感器库（SDR）读取失败，可登录但无法读传感器；需检查 BMC / 固件', 'BMC sensor repository (SDR) read failed: login works but sensors cannot be read; check BMC / firmware'),
    ipmi_collector_privilege_insufficient: tx('BMC 账号权限不足', 'BMC account privilege insufficient'),
    ipmi_collector_bmc_busy: tx('BMC 繁忙，会话资源不足', 'BMC busy: session resources exhausted'),
    ipmi_collector_failed: tx('BMC 可登录（bmc、chassis 正常），但传感器读取 collector=ipmi 失败', 'BMC login works (bmc, chassis ok) but sensor read collector=ipmi fails'),
    ipmi_collector_stale: tx('collector=ipmi 样本超过 5 分钟未更新', 'collector=ipmi sample older than 5 minutes'),
    ipmi_collector_discontinuous: tx('近 1 小时采样间隔过大或成功率低于 95%', 'Last-hour sample gap too large or success below 95%'),
    ipmi_collector_missing: tx('抓取成功但没有 collector=ipmi 指标', 'Scrape ok but no collector=ipmi series'),
    ipmi_collector_unverified: tx('collector=ipmi 同一实例结果冲突', 'Conflicting collector=ipmi results for one instance'),
  } as Record<string, string>)[code || ''];
  coverageRows.sort((left, right) => compareIP(left.node_ip, right.node_ip));
  const coverageName = definitions.find(([job]) => job === coverageJob)?.[1] || coverageJob;
  return <div className="grid"><Card className="span-12 capability-note"><Database size={19} /><div><CardHead code="MONITORING-ONLY" title={tx('监控数据质量发现', 'Monitoring Data Quality Detection')} /><p>{tx('在用 GPU 资产 · 缺失 / 失效 / 漂移 / 覆盖异常', 'In-use GPU assets · missing / stale / drift / coverage gaps')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>{definitions.map(([job, name, use]) => { const rows = targets.filter(x => x.job === job); const up = rows.filter(x => x.health === 'up').length; const down = rows.length - up; return <button type="button" className={`card metric quality compact-quality-metric coverage-metric metric-selectable${coverageJob === job ? ' active' : ''}`} key={job} onClick={() => setCoverageJob(job)}><Server size={17} /><Badge value={inventoryError ? 'API ERROR' : down ? `${down} NOT UP` : rows.length ? 'ALL UP' : 'WAITING'} kind={inventoryError || down ? 'warning' : rows.length ? 'healthy' : 'neutral'} /><strong>{rows.length ? up : '—'}</strong><b>{name}</b><small>{use} · {rows.length || summary?.nodes.total || '—'} TARGETS</small></button>; })}
    <Card className="span-12"><CardHead code="TARGET ISSUES" title={coverageJob === 'node_exporter' ? tx('Node Exporter 未 UP 节点', 'Node Exporter NOT UP Nodes') : tx(`Node Exporter 正常但 ${coverageName} 未 UP 的节点`, `Node Exporter up but ${coverageName} not up`)} action={<div className="table-actions"><Badge value={coverageJob.toUpperCase()} kind="info" /><Badge value={`${coverageRows.length} NODES`} kind={coverageRows.length ? 'warning' : 'healthy'} /></div>} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>TARGET</th><th>{tx('状态', 'Health')}</th><th>{tx('原因', 'Reason')}</th><th>{tx('抑制', 'Suppression')}</th><th>{tx('原始错误', 'Last Error')}</th><th>{tx('同步时间', 'Synced')}</th></tr></thead><tbody>{coverageRows.map(row => <tr key={`${row.job}-${row.node_ip}`}><td><b>{row.node_ip}</b><small>{row.target_ip || '—'}</small></td><td><code>{row.job}</code></td><td><Badge value={row.health.toUpperCase()} kind={row.health === 'down' ? 'danger' : 'warning'} /></td><td><code>{row.reason_code || 'unclassified'}</code>{ipmiReasonHint(row.reason_code) && <small>{ipmiReasonHint(row.reason_code)}</small>}</td><td>{row.suppressed ? <Badge value={row.suppression_reason || 'SUPPRESSED'} kind="info" /> : <Badge value="ACTIONABLE" kind="warning" />}</td><td><small>{row.last_error || '—'}</small></td><td>{time(row.last_synced_at, lang)}</td></tr>)}</tbody></table>{coverageRows.length === 0 && <Empty tx={tx} title={coverageJob === 'node_exporter' ? tx('所有节点 Node Exporter 均正常', 'Every node has Node Exporter up') : tx(`Node Exporter 正常的节点 ${coverageName} 均正常`, `Every Node Exporter-up node has ${coverageName} up`)} />}</div></Card>
  </div>;
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
      <p className="settings-intro">{tx('仅影响显示名称', 'Display identity only')}</p>
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
    <LXOPAssetSettings tx={tx} />
    <MonitoringSettings tx={tx} />
    <AssetAuditSettings tx={tx} />
  </div>;
}

const emptyAssetConfig: LXOPAssetConfig = {
  auth_type: 'oauth_client_credentials', token_url: '', client_id_configured: false, client_secret_configured: false, ca_cert_pem: '',
  ops_host_url: '', asset_machine_url: '', data_center_id: '', insecure_skip_verify: false,
  enabled: true, api_key_configured: false, last_sync_status: 'not_configured',
  last_ops_host_count: 0, last_machine_count: 0,
};
function LXOPAssetSettings({ tx }: { tx: Tx }) {
  const empty = emptyAssetConfig;
  const [config, setConfig] = useState<LXOPAssetConfig>(empty);
  const [apiKey, setAPIKey] = useState('');
  const [clientID, setClientID] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [assetConfigLoaded, setAssetConfigLoaded] = useState(false);
  const [adminToken, setAdminToken] = useAdminToken();
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void fetch('/api/v1/platform-config/assets').then(async response => {
      const payload = await response.json();
      if (!response.ok || !payload.data) throw new Error(`HTTP ${response.status}`);
      if (!cancelled) { setConfig({ ...empty, ...payload.data }); setAssetConfigLoaded(true); }
    }).catch(() => { if (!cancelled) setError('asset_config_load_failed'); });
    return () => { cancelled = true; };
  }, [empty]);
  const update = <K extends keyof LXOPAssetConfig>(field: K, next: LXOPAssetConfig[K]) => setConfig(current => ({ ...current, [field]: next }));
  const save = async (syncOnly = false) => {
    setSaving(true); setMessage(''); setError('');
    try {
      const response = await fetch(`/api/v1/platform-config/assets${syncOnly ? '/sync' : ''}`, {
        method: syncOnly ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: syncOnly ? undefined : JSON.stringify({
          auth_type: config.auth_type, token_url: config.token_url, client_id: clientID, client_secret: clientSecret, ca_cert_pem: config.ca_cert_pem,
          ops_host_url: config.ops_host_url, asset_machine_url: config.asset_machine_url,
          data_center_id: config.data_center_id, api_key: apiKey,
          insecure_skip_verify: config.insecure_skip_verify, enabled: config.enabled,
        }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      if (syncOnly) {
        const current = await fetch('/api/v1/platform-config/assets');
        if (!current.ok) throw new Error(`HTTP ${current.status}`);
        const refreshed = await current.json(); setConfig({ ...empty, ...refreshed.data });
      } else setConfig({ ...empty, ...payload.data });
      setAPIKey(''); setClientID(''); setClientSecret('');
      setMessage(syncOnly ? tx('资产元数据同步完成', 'Asset metadata synchronized') : tx('资产源配置已加密保存', 'Asset source saved encrypted'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const oauth = config.auth_type === 'oauth_client_credentials';
  const valid = assetConfigLoaded && config.ops_host_url.trim() && config.asset_machine_url.trim() && config.data_center_id.trim() && adminToken.trim() && (oauth ? config.token_url.trim() && (clientID.trim() || config.client_id_configured) && (clientSecret.trim() || config.client_secret_configured) && (!clientID.trim() || !!clientSecret.trim()) : config.api_key_configured || apiKey.trim());
  return <>
    <Card className="span-8 lxop-settings">
      <CardHead code="ASSET SOURCE" title={tx('资产数据源', 'Asset Data Source')} action={<Badge value={config.enabled ? 'ENABLED' : 'DISABLED'} kind={config.enabled ? 'healthy' : 'neutral'} />} />
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('鉴权方式', 'Authentication')}</span><select aria-label={tx('鉴权方式', 'Authentication')} value={config.auth_type} onChange={event => { update('auth_type', event.target.value as LXOPAssetConfig['auth_type']); setAPIKey(''); setClientID(''); setClientSecret(''); }}><option value="oauth_client_credentials">OAuth 2.0 · Client Credentials</option><option value="api_key">X-API-Key · Legacy</option></select></label>
        <label><span>{tx('运维主机接口', 'Ops host endpoint')}<small>GET · FULL URL</small></span><input value={config.ops_host_url} onChange={event => update('ops_host_url', event.target.value)} placeholder="https://host:7090/openapi/v1/ops/host/list" /></label>
        <label><span>{tx('资产设备接口', 'Asset machine endpoint')}<small>GET · FULL URL</small></span><input value={config.asset_machine_url} onChange={event => update('asset_machine_url', event.target.value)} placeholder="https://host:7090/openapi/v1/asset/machine/list" /></label>
        <label><span>{tx('数据中心 ID', 'Data center ID')}<small>{oauth ? 'X-Dc-Id' : 'dataCenterId'}</small></span><input value={config.data_center_id} onChange={event => update('data_center_id', event.target.value)} /></label>
        {oauth ? <>
          <label><span>{tx('Token 接口', 'Token endpoint')}</span><input aria-label={tx('Token 接口', 'Token endpoint')} value={config.token_url} onChange={event => update('token_url', event.target.value)} placeholder="https://host/openapi/v1/oauth/token" /></label>
          <label><span>Client ID<small>{config.client_id_configured ? tx('已配置 · 留空保持', 'CONFIGURED · BLANK KEEPS CURRENT') : tx('首次配置必填', 'REQUIRED INITIALLY')}</small></span><input aria-label="Client ID" type="password" autoComplete="off" value={clientID} onChange={event => setClientID(event.target.value)} /></label>
          <label><span>Client Secret<small>{config.client_secret_configured ? tx('已加密保存 · 留空保持', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('首次配置必填', 'REQUIRED INITIALLY')}</small></span><input aria-label="Client Secret" type="password" autoComplete="new-password" value={clientSecret} onChange={event => setClientSecret(event.target.value)} /></label>
          <label><span>{tx('CA 证书（可选）', 'CA certificate (optional)')}</span><textarea aria-label={tx('CA 证书', 'CA certificate')} rows={3} value={config.ca_cert_pem} onChange={event => update('ca_cert_pem', event.target.value)} /></label>
        </> : <label><span>X-API-Key<small>{config.api_key_configured ? tx('已加密保存 · 留空保持不变', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('首次配置必填', 'REQUIRED INITIALLY')}</small></span><input type="password" autoComplete="new-password" value={apiKey} onChange={event => setAPIKey(event.target.value)} placeholder={config.api_key_configured ? '••••••••••••' : 'X-API-Key'} /></label>}
        <label><span>{tx('管理口令', 'Management token')}<small>X-Atlas-Admin-Token</small></span><input type="password" autoComplete="off" value={adminToken} onChange={event => setAdminToken(event.target.value)} /></label>
        <label className="settings-check"><span>{tx('连接选项', 'Connection options')}</span><span className="check-options"><input type="checkbox" checked={config.enabled} onChange={event => update('enabled', event.target.checked)} />{tx('启用资产源', 'Enable asset source')}<input type="checkbox" checked={config.insecure_skip_verify} onChange={event => update('insecure_skip_verify', event.target.checked)} />{tx('跳过证书验证（仅开发）', 'Skip certificate verification (development only)')}</span></label>
        {error && <p className="form-error">{error === 'asset_config_load_failed' ? tx('资产配置读取失败', 'Failed to load asset configuration') : error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('加密保存', 'Save Encrypted')}</button>
        <button className="primary-action" type="button" onClick={() => void save(true)} disabled={saving || !assetConfigLoaded || !adminToken.trim() || !config.enabled || config.last_sync_status === 'not_configured'}><RefreshCw size={15} />{tx('同步资产', 'Sync Assets')}</button>
      </form>
    </Card>
    <Card className="span-4 settings-preview lxop-status">
      <CardHead code="SOURCE STATUS" title={tx('资产同步状态', 'Asset Sync Status')} />
      <div className="preview-environment"><i /><span><b>{config.last_sync_status.toUpperCase()}</b><small>{config.last_sync_at ? time(config.last_sync_at) : tx('等待首次同步', 'Awaiting first sync')}</small></span></div>
      <dl>
        <div><dt>{tx('运维主机', 'Ops hosts')}</dt><dd>{config.last_ops_host_count}</dd></div>
        <div><dt>{tx('资产设备', 'Machines')}</dt><dd>{config.last_machine_count}</dd></div>
        <div><dt>{tx('鉴权方式', 'Authentication')}</dt><dd>{oauth ? 'OAuth 2.0' : 'X-API-Key'}</dd></div>
        <div><dt>{tx('凭据', 'Credentials')}</dt><dd>{(oauth ? config.client_secret_configured : config.api_key_configured) ? tx('已加密保存', 'Encrypted at rest') : tx('未配置', 'Not configured')}</dd></div>
        <div><dt>TLS</dt><dd>{config.insecure_skip_verify ? tx('开发兼容 · 未验证证书', 'Development · certificate unverified') : tx('验证证书', 'Certificate verification')}</dd></div>
      </dl>
      {config.last_sync_error && <p className="form-error">{config.last_sync_error}</p>}
    </Card>
  </>;
}

function FaultEventList({ tx, items }: { tx: Tx; items: FaultEvent[] }) { return <div className="event-list">{items.map(x => <div className="event-row" key={x.id}><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /><span><b>{x.rule_code}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.evidence}</small></span><code>×{x.occurrence_count}</code></div>)}{items.length === 0 && <Empty tx={tx} title={tx('无未恢复硬件事件', 'No open hardware events')} />}</div>; }
function Empty({ tx, title }: { tx: Tx; title: string }) { return <div className="empty"><Command size={20} /><b>{title}</b><span>{tx('等待数据', 'Waiting for data')}</span></div>; }

function GlobalSearch({ tx, query, setQuery, pagesCopy, assets, close, navigate }: { tx: Tx; query: string; setQuery: (v: string) => void; pagesCopy: ReturnType<typeof pageCopy>; assets: GPUAsset[]; close: () => void; navigate: (p: PageId, view?: string) => void }) {
  const input = useRef<HTMLInputElement>(null); useEffect(() => { input.current?.focus(); const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') close(); }; window.addEventListener('keydown', escape); return () => window.removeEventListener('keydown', escape); }, [close]);
  const q = query.trim().toLowerCase(); const pageRows = pages.filter(x => !q || `${pagesCopy[x].label} ${pagesCopy[x].title} ${x}`.toLowerCase().includes(q));
  const assetRows = q ? assets.filter(x => [x.node_ip, x.gpu_uuid, x.model_name, x.model, x.pci_bus_id, x.asset_key].join(' ').toLowerCase().includes(q)).slice(0, 8) : [];
  return <motion.div className="modal-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="modal-bg" onClick={close} aria-label={tx('关闭搜索', 'Close search')} /><motion.div className="search-modal" initial={{ y: -18, scale: .98 }} animate={{ y: 0, scale: 1 }}><div className="search-input"><Search size={18} /><input ref={input} value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('页面、节点 IP、GPU UUID', 'Pages, node IPs, GPU UUIDs')} /><kbd>ESC</kbd><button onClick={close} aria-label={tx('关闭搜索', 'Close search')}><X size={16} /></button></div><div className="search-results"><label>{tx('页面', 'PAGES')}</label>{pageRows.map(id => { const Icon = pageIcons[id]; return <button key={id} onClick={() => { navigate(id); close(); }}><Icon size={16} /><span><b>{pagesCopy[id].label}</b><small>{pagesCopy[id].desc}</small></span><ChevronRight size={14} /></button>; })}{assetRows.length > 0 && <label>{tx('GPU 资产', 'GPU ASSETS')}</label>}{assetRows.map(x => <button key={x.asset_key} onClick={() => { navigate('gpu', 'inventory'); close(); }}><Cpu size={16} /><span><b>{x.gpu_uuid || 'UUID UNKNOWN'}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.model_name || x.model || 'UNKNOWN'}</small></span><Badge value={x.state.toUpperCase()} kind={x.state === 'active' ? 'healthy' : 'warning'} /></button>)}{pageRows.length + assetRows.length === 0 && <Empty tx={tx} title={tx('无结果', 'No results')} />}</div></motion.div></motion.div>;
}

function IssueDrawer({ tx, detail, lang, close, saved }: { tx: Tx; detail: IssueDetail; lang: string; close: () => void; saved: () => void }) {
  const issue = detail.issue;
  const nodeEvidence = detail.node_evidence_collection;
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
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer issue-drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>ISSUE / #{issue.id} · {issue.category.toUpperCase()}</span><h2>{issue.title}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /><time>{time(issue.last_detected_at, lang)}</time></div><div className="detail-grid">{[[tx('节点', 'Node'), issue.node_ip], ['GPU UUID', issue.gpu_uuid], [tx('对象', 'Entity'), `${issue.entity_type} / ${issue.entity_key}`], [tx('检测来源', 'Detection Source'), issue.detection_source]].map(item => <div key={item[0]}><small>{item[0]}</small><b>{item[1] || '—'}</b></div>)}</div><section><CardHead code="EVIDENCE" title={tx('问题描述', 'Problem Evidence')} /><p className="muted">{issue.description || '—'}</p></section>{nodeEvidence ? <section><CardHead code="RECOVERY EVIDENCE" title={tx('节点恢复只读证据', 'Post-recovery Read-only Evidence')} action={<Badge value={nodeEvidence.status.toUpperCase()} kind={nodeEvidence.status === 'completed' ? 'healthy' : nodeEvidence.status === 'waiting_recovery' ? 'info' : 'warning'} />} /><div className="recovery-evidence-banner"><ShieldCheck size={17} /><span><b>{tx('以下为采集证据，不是自动根因结论', 'Collected evidence, not an automatic root-cause conclusion')}</b><small>#{nodeEvidence.id} · {nodeEvidence.trigger} · {nodeEvidence.read_only ? 'READ-ONLY' : 'EXECUTION UNKNOWN'}{nodeEvidence.retry_of_collection_id ? ` · RETRY OF #${nodeEvidence.retry_of_collection_id}` : ''} · {time(nodeEvidence.finished_at || nodeEvidence.started_at, lang)}</small></span></div>{nodeEvidence.status === 'waiting_recovery' ? <p className="muted">{tx('节点仍不可连接；恢复为可访问状态后将自动补采。', 'The node is still unreachable; evidence will be collected automatically after it becomes accessible.')}</p> : <div className="recovery-evidence-records">{(nodeEvidence.records || []).map(record => <article key={record.id}><header><code>{record.command_id}</code><Badge value={record.status.toUpperCase()} kind={record.status === 'completed' ? 'healthy' : 'warning'} /><small>{record.kind.toUpperCase()} · {record.output_bytes} B{record.truncated ? ' · TRUNCATED' : ''}</small></header><pre>{record.output || tx('无输出', 'No output')}</pre></article>)}{(nodeEvidence.records || []).length === 0 && <p className="muted">{nodeEvidence.failure_code ? `${tx('采集失败', 'Collection failed')}: ${nodeEvidence.failure_code}` : tx('暂无证据记录', 'No evidence records')}</p>}</div>}</section> : issue.issue_type === 'node_state' ? <section><CardHead code="RECOVERY EVIDENCE" title={tx('节点恢复只读证据', 'Post-recovery Read-only Evidence')} /><p className="muted">{issue.detection_state === 'active' ? tx('节点仍处于失联状态；恢复后将自动采集启动历史、GPU 快照和时间窗内核日志。', 'The node is still unavailable. Boot history, GPU snapshots, and bounded kernel logs will be collected automatically after recovery.') : tx('该问题暂无关联的恢复证据任务。', 'No post-recovery evidence collection is linked to this issue yet.')}</p></section> : null}<section><CardHead code="HUMAN FEEDBACK" title={tx('补充原因与解决过程', 'Root Cause & Resolution')} /><div className="resolution-form"><label>{tx('处理状态', 'Status')}<select value={status} onChange={event => setStatus(event.target.value)}><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><label>{tx('根本原因', 'Root cause')}<textarea value={rootCause} onChange={event => setRootCause(event.target.value)} placeholder={tx('问题为什么发生？需写证据，不只写现象', 'Why did it happen? Record evidence, not only symptoms.')} /></label><label>{tx('解决方案', 'Solution')}<textarea value={solution} onChange={event => setSolution(event.target.value)} placeholder={tx('采用了什么修复方案？', 'What remediation was chosen?')} /></label><label>{tx('解决过程', 'Resolution process')}<textarea value={process} onChange={event => setProcess(event.target.value)} placeholder={tx('按顺序记录操作、验证与回滚点', 'Record actions, validation and rollback points in order.')} /></label><label>{tx('处理结果', 'Result')}<textarea value={result} onChange={event => setResult(event.target.value)} placeholder={tx('是否恢复、如何验证、是否遗留风险', 'Recovery state, validation evidence and remaining risk.')} /></label><label>{tx('处理人', 'Operator')}<input value={operator} onChange={event => setOperator(event.target.value)} placeholder={tx('姓名或工号', 'Name or operator ID')} /></label><label className="training-check"><input type="checkbox" checked={trainingEligible} onChange={event => { setTrainingEligible(event.target.checked); if (event.target.checked) setStatus('resolved'); }} /><span>{tx('内容完整且已脱敏，可进入 AI/Skill 训练数据集', 'Complete and sanitized; eligible for AI/Skill training dataset')}</span></label>{error && <p className="form-error">{error}</p>}<button className="primary-action" onClick={submit} disabled={saving}>{saving ? tx('保存中…', 'Saving…') : tx('保存处置记录', 'Save resolution')}</button></div></section><section><CardHead code="HISTORY" title={tx('处置历史', 'Resolution History')} action={<Badge value={`${detail.resolutions.length} RECORDS`} kind="info" />} /><div className="resolution-history">{detail.resolutions.map(item => <article key={item.id}><header><Badge value={item.status.toUpperCase()} kind={issueTone(item.status)} /><b>{item.operator}</b><time>{time(item.created_at, lang)}</time>{item.training_eligible && <Badge value="TRAINING" kind="info" />}</header><p><strong>{tx('原因', 'Cause')}:</strong> {item.root_cause || '—'}</p><p><strong>{tx('方案', 'Solution')}:</strong> {item.solution || '—'}</p><p><strong>{tx('过程', 'Process')}:</strong> {item.resolution_process || '—'}</p><p><strong>{tx('结果', 'Result')}:</strong> {item.result || '—'}</p></article>)}{detail.resolutions.length === 0 && <p className="muted">{tx('尚无人工处置记录', 'No human resolution records yet')}</p>}</div></section></div></motion.aside></motion.div>;
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
          <div className="report-banner"><div><ShieldCheck size={18} /><span><b>{tx('只读确定性分析', 'Read-only deterministic analysis')}</b><small>{tx('只读汇总，未执行任何操作', 'Read-only summary; no actions executed')}</small></span></div><div><Badge value="DETERMINISTIC" kind="info" /><Badge value="READ-ONLY" kind="healthy" /><Badge value={report.no_action_executed ? 'NO ACTION EXECUTED' : 'ACTION UNKNOWN'} kind={report.no_action_executed ? 'healthy' : 'warning'} /></div></div>
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

