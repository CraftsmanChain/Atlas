import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';
import {
  Activity, AlertTriangle, BarChart3, Bell, BookOpen, BrainCircuit, CheckCircle2,
  ChevronLeft, ChevronRight, CircleGauge, ClipboardList, Command, Cpu, Database, Eye, Filter, Gauge,
  Languages, Layers3, MemoryStick, Menu, Moon, Network,
  Palette, RefreshCw, Save, Search, Server, ShieldAlert, ShieldCheck, Sun,
  Thermometer, X, Zap,
} from 'lucide-react';
import './App.css';

type PageId = 'overview' | 'gpus' | 'issues' | 'incidents' | 'validations' | 'quality' | 'models' | 'about';
type SubPage = { id: string; label: string };
type Tx = (zh: string, en: string) => string;

type Ingestion = {
  id: number; event_id: string; source: string; host: string; level: string; message: string;
  process_status: string; callback_status: string; raw_payload?: string;
  labels?: Record<string, string>; created_at: string; ai_report_status?: string;
  ai_report_summary?: string; ai_report_confidence?: number;
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
type FreshnessSource = { status: string; observed_at?: string; age_seconds?: number; stale_after_seconds: number; source_mode?: string; message?: string };
type DataFreshness = { overall_status: string; server_time: string; sources: Record<string, FreshnessSource> };
type GPUAsset = { id: number; asset_key: string; node_id: number; node_ip: string; gpu_index: number; gpu_uuid: string; device: string; model: string; model_name: string; pci_bus_id: string; host_serial: string; driver_version: string; state: string; sample_state: string; last_seen_at: string; last_synced_at: string };
type CollectorTarget = { id: number; job: string; instance: string; target_ip: string; node_ip: string; health: string; reason_code?: string; suppressed: boolean; suppression_reason?: string; last_error?: string; last_scrape_at?: string; last_synced_at: string };
type SyncRun = { id: number; task_type: string; status: string; node_count: number; gpu_count: number; known_uuid_count: number; target_count: number; change_count: number; started_at: string; finished_at?: string; error_message?: string };
type AssetChange = { id: number; sync_run_id: number; event_type: string; node_ip: string; asset_key: string; old_value: string; new_value: string; source: string; created_at: string };
type GPUHealthScore = { id: number; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; score: number | null; level: string; data_confidence: string; stability_score: number; memory_score: number; thermal_score: number; power_score: number; interconnect_score: number; performance_score: number; evidence: string[]; metric_sources?: Record<string, string>; sources_available?: string[]; fallback_metric_count?: number; consistency_issues?: string[]; consistency_issue_count?: number; rule_version: string; evaluated_at: string };
type HealthSummary = { total: number; scored: number; unknown: number; average_score: number; by_level: Record<string, number>; by_confidence: Record<string, number>; latest_run: { id: number; rule_version: string; status: string; rule_hit_count: number; finished_at?: string } | null };
type FaultEvent = { id: number; source: string; state: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; rule_code: string; domain: string; severity: string; evidence: string; observed_value: number; threshold: string; occurrence_count: number; rule_version: string; first_observed_at: string; last_observed_at: string; recovered_at?: string };
type CursorMeta = { total: number; limit: number; has_more: boolean; next_before_id: number };
type FaultEventSummary = { total: number; open: number; by_state: Record<string, number>; open_by_severity: Record<string, number> };
type PlatformIssue = { id: number; issue_key: string; category: string; issue_type: string; title: string; description: string; entity_type: string; entity_key: string; node_ip: string; gpu_uuid: string; severity: string; status: string; detection_state: string; detection_source: string; source_record_id: number; first_detected_at: string; last_detected_at: string; source_recovered_at?: string; resolved_at?: string; latest_resolution_id: number };
type IssueResolution = { id: number; issue_id: number; status: string; root_cause: string; solution: string; resolution_process: string; result: string; evidence: string[]; operator: string; training_eligible: boolean; created_at: string };
type IssueDetail = { issue: PlatformIssue; resolutions: IssueResolution[] };
type IssueSummary = { discovered: number; resolved: number; remaining: number; ignored: number; active_detection: number; by_category: Record<string, number>; by_status: Record<string, number>; by_severity: Record<string, number>; generated_at: string };
type FleetSummary = {
  nodes: { total: number; by_state: Record<string, number> };
  gpus: { total: number; known_uuid: number; unknown_uuid: number; by_state: Record<string, number> };
  targets: { by_health: Record<string, number> };
  latest_sync: SyncRun | null;
};

const pageIcons = { overview: BarChart3, gpus: Cpu, issues: ClipboardList, incidents: Bell, validations: Gauge, quality: Database, models: BrainCircuit, about: BookOpen };
const pages: PageId[] = ['overview', 'gpus', 'issues', 'incidents', 'validations', 'quality', 'models', 'about'];
const pageCopy = (tx: Tx): Record<PageId, { label: string; group: string; title: string; desc: string }> => ({
  overview: { label: tx('集群总览', 'Overview'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 集群', 'GPU Fleet'), desc: tx('资产、状态、事件与交付进度', 'Assets, status, incidents and delivery') },
  gpus: { label: tx('GPU 资产', 'GPU Assets'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 资产', 'GPU Assets'), desc: tx('健康、异常、性能与取值来源', 'Health, anomaly, performance and source lineage') },
  issues: { label: tx('问题中心', 'Issue Center'), group: tx('运行', 'OPERATIONS'), title: tx('问题统计与处置', 'Issue Analytics & Resolution'), desc: tx('发现、分类、状态、原因、解决过程与训练数据', 'Discovery, categories, status, root cause, resolution process and training data') },
  incidents: { label: tx('事件', 'Incidents'), group: tx('运行', 'OPERATIONS'), title: tx('事件', 'Incidents'), desc: tx('XID、ECC、掉卡、不可用与处理状态', 'XID, ECC, dropout, unavailable and workflow state') },
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
  quality: [{ id: 'targets', label: tx('采集覆盖', 'Target Coverage') }, { id: 'identity', label: tx('身份与带外', 'Identity & BMC') }, { id: 'audit', label: tx('同步审计', 'Sync Audit') }],
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
  const [subPage, setSubPage] = useState<Record<PageId, string>>({ overview: '', gpus: 'health', issues: '', incidents: 'hardware', validations: 'degradation', quality: 'targets', models: 'stack', about: 'definition' });

  const navigate = (id: PageId) => { setPage(id); setSidebar(false); window.location.hash = `/${id}`; window.scrollTo(0, 0); };
  const load = async () => {
    setLoading(true);
    try {
      const [s, df, f, fs, ga, ct, sr, ac, hs, hg, fes, is, pc] = await Promise.all([
        fetch('/api/v1/status'), fetch('/api/v1/data-freshness'), fetch('/api/v1/alerts/failures?limit=8'),
        fetch('/api/v1/fleet/summary'), fetch('/api/v1/gpus?limit=2000'), fetch('/api/v1/targets?limit=2000'),
        fetch('/api/v1/sync-runs?limit=20'), fetch('/api/v1/inventory/changes?limit=50'),
        fetch('/api/v1/health/summary'), fetch('/api/v1/health/gpus?limit=1000'), fetch('/api/v1/fault-events/summary'), fetch('/api/v1/issues/summary'),
        fetch('/api/v1/platform-config'),
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
  }, [faultBeforeID, query, level, ingestionRefresh]);
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
  const freshnessTone = freshnessStatus === 'FRESH' ? 'healthy' : freshnessStatus === 'PARTIAL' || freshnessStatus === 'SNAPSHOT' ? 'info' : freshnessStatus === 'STALE' ? 'warning' : freshnessStatus === 'ERROR' ? 'danger' : 'neutral';
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
      <nav>{['运行', '系统'].map(group => <div className="nav-group" key={group}><label>{tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')}</label>{pages.filter(id => copy[id].group === tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')).map(id => { const Icon = pageIcons[id]; const count = id === 'issues' ? issueSummary?.remaining : id === 'incidents' ? faultSummary?.open : 0; return <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={16} /><span>{copy[id].label}</span>{(count || 0) > 0 && <em>{count}</em>}</button>; })}</div>)}</nav>
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
        <div className="page-head"><div><h1>{copy[page].title}</h1><p>{copy[page].desc}</p></div><div className="page-meta"><Badge value={platformConfig.environment} kind="info" /><Badge value={`DATA ${freshnessStatus}`} kind={freshnessTone} /><span title={tx('源数据最新观测时间；不是页面刷新时间', 'Latest source observation; not the page refresh time')}>{freshnessLatest ? time(freshnessLatest, lang) : '—'}</span></div></div>
        {currentSubPages.length > 0 && <nav className="subnav" aria-label={tx('页面分区', 'Page sections')}>{currentSubPages.map(item => <button key={item.id} className={subPage[page] === item.id ? 'active' : ''} onClick={() => setSubPage(current => ({ ...current, [page]: item.id }))}>{item.label}</button>)}</nav>}
        <AnimatePresence mode="wait"><motion.div key={page} className={page === 'overview' ? 'overview-page' : 'secondary-page'} initial={{ opacity: 0, y: 5 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: .12 }}>
          {page === 'overview' && <Overview tx={tx} faults={openFaults} faultSummary={faultSummary} hosts={hosts} loading={loading} summary={summary} models={fleetModels} inventoryError={inventoryError} navigate={navigate} />}
          {page === 'gpus' && <Gpus tx={tx} view={subPage.gpus} assets={gpuAssets} models={fleetModels} loading={loading} inventoryError={inventoryError} healthScores={healthScores} healthSummary={healthSummary} lang={lang} />}
          {page === 'issues' && <Issues tx={tx} summary={issueSummary} rows={issues} meta={issueMeta} page={issueCursorHistory.length + 1} loading={issueLoading} category={issueCategory} setCategory={setIssueCategory} status={issueStatus} setStatus={setIssueStatus} query={issueQuery} setQuery={setIssueQuery} previousPage={previousIssuePage} nextPage={nextIssuePage} select={setSelectedIssueID} lang={lang} />}
          {page === 'incidents' && <Incidents tx={tx} view={subPage.incidents} rows={filtered} ingestionMeta={ingestionMeta} ingestionPage={ingestionCursorHistory.length + 1} ingestionLoading={ingestionLoading} previousIngestionPage={previousIngestionPage} nextIngestionPage={nextIngestionPage} faultRows={faultEvents} faultMeta={faultMeta} faultPage={faultCursorHistory.length + 1} faultLoading={faultLoading} previousFaultPage={previousFaultPage} nextFaultPage={nextFaultPage} query={subPage.incidents === 'ingestion' ? ingestionQuery : query} setQuery={subPage.incidents === 'ingestion' ? setIngestionQuery : setQuery} level={subPage.incidents === 'ingestion' ? ingestionLevel : level} setLevel={subPage.incidents === 'ingestion' ? setIngestionLevel : setLevel} failures={failures} select={setSelected} lang={lang} />}
          {page === 'validations' && <Validations tx={tx} view={subPage.validations} />}
          {page === 'quality' && <Quality tx={tx} view={subPage.quality} targets={targets} summary={summary} inventoryError={inventoryError} syncRuns={syncRuns} assetChanges={assetChanges} lang={lang} />}
          {page === 'models' && <Models tx={tx} view={subPage.models} />}
          {page === 'about' && <About tx={tx} view={subPage.about} platformConfig={platformConfig} onPlatformConfig={setPlatformConfig} />}
        </motion.div></AnimatePresence>
      </div>
    </main>
    <AnimatePresence>{searchOpen && <GlobalSearch tx={tx} query={query} setQuery={setQuery} pagesCopy={copy} items={ingestions} assets={gpuAssets} close={() => setSearchOpen(false)} navigate={navigate} select={id => { setSelected(id); navigate('incidents'); setSearchOpen(false); }} />}</AnimatePresence>
    <AnimatePresence>{selectedItem && <Drawer tx={tx} item={selectedItem} report={report} lang={lang} close={() => setSelected(null)} />}</AnimatePresence>
    <AnimatePresence>{selectedIssueID && issueDetail && <IssueDrawer tx={tx} detail={issueDetail} lang={lang} close={() => setSelectedIssueID(null)} saved={() => { setIssueRefresh(value => value + 1); void load(); }} />}</AnimatePresence>
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
    ['availability', tx('节点可用性', 'Node Availability')],
    ['inventory', tx('资产与身份', 'Inventory & Identity')],
    ['data_quality', tx('数据质量', 'Data Quality')],
    ['hardware_fault', tx('硬件故障', 'Hardware Fault')],
  ];
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
    <Card className="span-12"><CardHead code="CATEGORIES" title={tx('问题分类', 'Issue Categories')} action={<Badge value={`${summary?.discovered || 0} TOTAL`} kind="info" />} /><div className="issue-categories"><button className={!category ? 'active' : ''} onClick={() => setCategory('')}><b>{tx('全部问题', 'All Issues')}</b><strong>{summary?.discovered || 0}</strong></button>{categories.map(([key, label]) => <button key={key} className={category === key ? 'active' : ''} onClick={() => setCategory(key)}><b>{label}</b><strong>{summary?.by_category[key] || 0}</strong><small>{key}</small></button>)}</div></Card>
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

function Incidents({ tx, view, rows, ingestionMeta, ingestionPage, ingestionLoading, previousIngestionPage, nextIngestionPage, faultRows, faultMeta, faultPage, faultLoading, previousFaultPage, nextFaultPage, query, setQuery, level, setLevel, failures, select, lang }: { tx: Tx; view: string; rows: Ingestion[]; ingestionMeta: IngestionMeta; ingestionPage: number; ingestionLoading: boolean; previousIngestionPage: () => void; nextIngestionPage: () => void; faultRows: FaultEvent[]; faultMeta: CursorMeta; faultPage: number; faultLoading: boolean; previousFaultPage: () => void; nextFaultPage: () => void; query: string; setQuery: (v: string) => void; level: string; setLevel: (v: string) => void; failures: Failure[]; select: (id: number) => void; lang: string }) {
  if (view === 'workflow') return <div className="grid"><Card className="span-12"><CardHead code="WORKFLOW" title={tx('处理状态', 'Workflow State')} action={failures.length ? <Badge value={`${failures.length} INGEST FAILED`} kind="warning" /> : undefined} /><div className="workflow">{[tx('发现', 'DETECTED'), tx('分诊', 'TRIAGED'), tx('待维护', 'PENDING'), tx('已隔离', 'ISOLATED'), tx('诊断', 'DIAGNOSIS'), tx('修复', 'REPAIRED'), tx('复测', 'VALIDATED'), tx('关闭', 'CLOSED')].map((x, i) => <div key={x}><small>{String(i + 1).padStart(2, '0')}</small><b>{x}</b>{i < 7 && <ChevronRight size={13} />}</div>)}</div></Card></div>;
  if (view === 'ingestion') {
    const status = ingestionMeta.stream_status.toUpperCase();
    const statusTone = status === 'LIVE' ? 'healthy' : status === 'SNAPSHOT' ? 'info' : status === 'ERROR' ? 'danger' : status === 'STALE' ? 'warning' : 'neutral';
    const start = ingestionMeta.total > 0 ? (ingestionPage - 1) * ingestionMeta.limit + 1 : 0;
    const end = start > 0 ? start + rows.length - 1 : 0;
    return <div className="grid">
      <Card className="span-12 ingestion-stream"><div><CardHead code="INGESTION STREAM" title={tx('告警接收链路', 'Alert Ingestion Stream')} /><p>{tx('原始监控告警接收与持久化审计；不等同于 Atlas 硬件事件。', 'Raw monitoring alert ingestion and persistence audit; separate from Atlas hardware events.')}</p></div><div className="ingestion-stream-metrics"><span><small>{tx('状态', 'Status')}</small><Badge value={ingestionLoading ? 'SYNCING' : status} kind={ingestionLoading ? 'info' : statusTone} /></span><span><small>{tx('数据模式', 'Data mode')}</small><b>{ingestionMeta.source_mode.toUpperCase()}</b></span><span><small>{tx('最新接收', 'Latest received')}</small><b>{ingestionMeta.latest_received_at ? time(ingestionMeta.latest_received_at, lang) : '—'}</b></span><span><small>{tx('近 5 分钟', 'Last 5m')}</small><b>{ingestionMeta.received_5m}</b></span><span><small>{tx('近 1 小时', 'Last 1h')}</small><b>{ingestionMeta.received_1h}</b></span><span><small>{tx('全部记录', 'All records')}</small><b>{ingestionMeta.all_total}</b></span></div></Card>
      <Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('主机 / UUID / XID / 消息', 'Host / UUID / XID / message')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'error', 'warning', 'info'].map(x => <option key={x}>{x}</option>)}</select></label><span>{start}–{end} / {ingestionMeta.total}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('事件', 'Incident')}</th><th>{tx('主机 / GPU', 'Host / GPU')}</th><th>{tx('来源', 'Source')}</th><th>{tx('处理', 'State')}</th><th>{tx('时间', 'Time')}</th><th /></tr></thead><tbody>{rows.map(x => <tr key={x.id}><td><Badge value={x.level || 'unknown'} kind={tone(x.level)} /></td><td><b>{x.message}</b><small>#{x.id} · {x.labels?.err_msg || x.event_id}</small></td><td>{x.host || '—'}<small><code>{x.labels?.UUID || x.labels?.pci_bus_id || 'UNMAPPED'}</code></small></td><td>{x.source}</td><td><Badge value={x.process_status} kind={x.process_status === 'success' ? 'healthy' : 'warning'} /></td><td>{time(x.created_at, lang)}</td><td><button className="link" onClick={() => select(x.id)}>{tx('详情', 'Details')}<Eye size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('无匹配接收记录', 'No matching ingestion records')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${ingestionPage} 页`, `Page ${ingestionPage}`)}</span><div><button onClick={previousIngestionPage} disabled={ingestionPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextIngestionPage} disabled={!ingestionMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card>
    </div>;
  }
  const faultStart = faultMeta.total > 0 ? (faultPage - 1) * faultMeta.limit + 1 : 0;
  const faultEnd = faultStart > 0 ? faultStart + faultRows.length - 1 : 0;
  return <div className="grid"><Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('节点 / UUID / 规则 / 详情', 'Node / UUID / rule / details')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'warning', 'attention'].map(x => <option key={x}>{x}</option>)}</select></label><span>{faultLoading ? tx('加载中', 'Loading') : `${faultStart}–${faultEnd} / ${faultMeta.total}`}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('规则 / 详情', 'Rule / Details')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('维度', 'Domain')}</th><th>{tx('状态', 'State')}</th><th>{tx('次数', 'Count')}</th><th>{tx('首次 / 最近', 'First / Last')}</th></tr></thead><tbody>{faultRows.map(x => <tr key={x.id}><td><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /></td><td><b>{x.rule_code}</b><small>{x.evidence} · {tx('阈值', 'threshold')} {x.threshold}</small></td><td><b>{x.node_ip} · GPU {x.gpu_index}</b><small><code>{x.gpu_uuid}</code></small></td><td><code>{x.domain.toUpperCase()}</code></td><td><Badge value={x.state.toUpperCase()} kind={tone(x.state)} /></td><td>{x.occurrence_count}</td><td>{time(x.first_observed_at, lang)}<small>{time(x.last_observed_at, lang)}</small></td></tr>)}</tbody></table>{faultRows.length === 0 && <Empty tx={tx} title={tx('无匹配硬件事件', 'No matching hardware events')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${faultPage} 页`, `Page ${faultPage}`)}</span><div><button onClick={previousFaultPage} disabled={faultPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextFaultPage} disabled={!faultMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card></div>;
}

function Validations({ tx, view }: { tx: Tx; view: string }) { return <div className="grid"><Card className="span-12 safety"><ShieldAlert size={21} /><div><CardHead code="SAFETY" title={tx('主动测试前置条件', 'Active Test Gates')} /><p>{tx('人工确认维护窗口 · 无 GPU 计算进程 · 操作确认 · 超时与温控保护', 'Maintenance confirmed · no GPU compute process · operator confirmation · timeout and thermal guard')}</p></div><Badge value="NO AUTO STRESS" kind="warning" /></Card>{view === 'records' ? <Card className="span-12"><CardHead code="QUEUE" title={tx('验证记录', 'Validation Records')} /><Empty tx={tx} title={tx('验证接口未启用', 'Validation API disabled')} /></Card> : <Card className="span-12"><CardHead code="DETECTION" title={tx('算力衰减', 'Performance Degradation')} /><div className="stages"><div><b>01 / PASSIVE</b><h3>{tx('硬件侧候选', 'Hardware Candidate')}</h3><p>effective clock · power · thermal · throttle · PCIe / NVLink</p><Badge value="NON-INTRUSIVE" kind="healthy" /></div><ChevronRight /><div><b>02 / ACTIVE</b><h3>{tx('维护复测', 'Maintenance Validation')}</h3><p>GEMM · memory bandwidth · PCIe · NVLink / NCCL</p><Badge value="MAINTENANCE REQUIRED" kind="warning" /></div></div></Card>}</div>; }
function Quality({ tx, view, targets, summary, inventoryError, syncRuns, assetChanges, lang }: { tx: Tx; view: string; targets: CollectorTarget[]; summary: FleetSummary | null; inventoryError: boolean; syncRuns: SyncRun[]; assetChanges: AssetChange[]; lang: string }) {
  const definitions = [['dcgm_exporter', 'DCGM Exporter', 'GPU Telemetry'], ['gpu_exporter', 'GPU Exporter', 'NVML Extension'], ['node_exporter', 'Node Exporter', 'Host OS'], ['ipmi_exporter', 'IPMI Exporter', 'BMC / Hardware']] as const;
  if (view === 'identity') return <div className="grid"><Card className="span-12"><CardHead code="IDENTITY" title={tx('资产身份映射', 'Asset Identity')} /><div className="identity">{['HOST', 'BMC', 'CHASSIS', 'PCIE SLOT', 'BUS ID', 'GPU UUID'].map((x, i) => <span key={x}>{x}{i < 5 && <ChevronRight size={13} />}</span>)}</div></Card><Card className="span-12"><CardHead code="BMC / IPMI" title={tx('带外数据', 'Out-of-band Data')} /><div className="check-grid">{['SEL', 'PSU / VOLTAGE', 'FAN / THERMAL', 'POWER', 'PCIE SLOT', 'MEMORY CE / UE'].map(x => <span key={x}><CheckCircle2 size={14} />{x}</span>)}</div></Card></div>;
  if (view === 'audit') return <div className="grid">
    <Card className="span-12"><CardHead code="SYNC RUNS" title={tx('同步批次', 'Reconciliation Runs')} action={<Badge value={`${syncRuns.length} RUNS`} kind="info" />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('任务', 'Task')}</th><th>{tx('状态', 'Status')}</th><th>{tx('节点 / GPU / UUID', 'Nodes / GPUs / UUIDs')}</th><th>TARGETS</th><th>{tx('变化', 'Changes')}</th><th>{tx('开始时间', 'Started')}</th><th>{tx('耗时', 'Duration')}</th></tr></thead><tbody>{syncRuns.map(run => <tr key={run.id}><td><code>#{run.id}</code></td><td><b>{run.task_type || 'legacy'}</b></td><td><Badge value={run.status.toUpperCase()} kind={run.status === 'success' ? 'healthy' : run.status === 'failed' ? 'danger' : 'warning'} /></td><td>{run.node_count} / {run.gpu_count} / {run.known_uuid_count}</td><td>{run.target_count}</td><td>{run.change_count}</td><td>{time(run.started_at, lang)}</td><td>{run.finished_at ? `${Math.max(0, (new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1)}s` : '—'}</td></tr>)}</tbody></table>{syncRuns.length === 0 && <Empty tx={tx} title={tx('暂无同步批次', 'No sync runs')} />}</div></Card>
    <Card className="span-12"><CardHead code="CHANGE LOG" title={tx('资产变更', 'Asset Changes')} action={<Badge value={`${assetChanges.length} EVENTS`} kind={assetChanges.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('事件', 'Event')}</th><th>{tx('节点', 'Node')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('原值', 'Before')}</th><th>{tx('新值', 'After')}</th><th>RUN</th><th>{tx('时间', 'Time')}</th></tr></thead><tbody>{assetChanges.map(change => <tr key={change.id}><td><code>#{change.id}</code></td><td><Badge value={change.event_type.toUpperCase()} kind={change.event_type.includes('retired') ? 'danger' : 'warning'} /></td><td><b>{change.node_ip}</b></td><td><code>{change.asset_key}</code></td><td><code>{change.old_value || '—'}</code></td><td><code>{change.new_value || '—'}</code></td><td>#{change.sync_run_id || '—'}</td><td>{time(change.created_at, lang)}</td></tr>)}</tbody></table>{assetChanges.length === 0 && <Empty tx={tx} title={tx('暂无资产变化', 'No asset changes')} />}</div></Card>
  </div>;
  const problemTargets = targets.filter(target => target.health !== 'up');
  return <div className="grid"><Card className="span-12 capability-note"><Database size={19} /><div><CardHead code="MONITORING-ONLY" title={tx('监控数据质量发现', 'Monitoring Data Quality Detection')} /><p>{tx('无需登录节点即可基于 Prometheus 指标、Target、时序连续性和资产对账识别缺失、失效、漂移与覆盖异常。平台负责发现、定位和记录问题，不自动修改 exporter、监控配置或节点。', 'Without node access, Atlas can use Prometheus metrics, targets, time-series continuity and inventory reconciliation to detect missing, stale, drifting or uncovered telemetry. It identifies and records issues; it does not automatically modify exporters, monitoring configuration or nodes.')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>{definitions.map(([job, name, use]) => { const rows = targets.filter(x => x.job === job); const up = rows.filter(x => x.health === 'up').length; const down = rows.length - up; return <Card className="metric quality" key={job}><Server size={17} /><Badge value={inventoryError ? 'API ERROR' : down ? `${down} NOT UP` : rows.length ? 'ALL UP' : 'WAITING'} kind={inventoryError || down ? 'warning' : rows.length ? 'healthy' : 'neutral'} /><strong>{rows.length ? up : '—'}</strong><b>{name}</b><small>{use} · {rows.length || summary?.nodes.total || '—'} TARGETS</small></Card>; })}
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
    <Card className="span-5"><CardHead code="BOUNDARIES" title={tx('工程边界', 'Engineering Boundaries')} /><div className="rules danger-list">{[tx('无节点登录权限时仍可使用监控数据模式', 'Monitoring-only mode does not require node access'), tx('平台发现数据问题，不自动修复监控配置', 'Detect data issues; do not auto-remediate monitoring'), tx('未确认维护窗口时禁止主动压测', 'No active stress before maintenance confirmation'), tx('LLM 不直接评分', 'LLM does not score hardware'), tx('异常分不等于故障概率', 'Anomaly score is not failure probability'), tx('缺失数据输出 unknown', 'Missing data returns unknown'), tx('重启、隔离、复测需审批', 'Restart, isolation and validation require approval')].map(x => <span key={x}><AlertTriangle size={15} />{x}</span>)}</div></Card>
    <Card className="span-12"><CardHead code="VERSIONING" title={tx('模块化版本治理', 'Modular Version Governance')} /><div className="version-policy"><span><b>MAJOR</b>{tx('模块契约或核心语义不兼容变更', 'Incompatible module contract or semantics')}</span><span><b>MINOR</b>{tx('新增向后兼容能力', 'Backward-compatible capability')}</span><span><b>PATCH</b>{tx('规则、阈值、缺陷与展示修正', 'Rules, thresholds, fixes and presentation')}</span></div></Card>
  </div>;

  const modules = [
    { id: 'asset', name: tx('资产对账', 'Asset Reconciliation'), version: 'v0.2.0', status: tx('监控侧基线', 'MONITORING BASELINE'), desc: tx('以 LXOPEasier 的机房、机柜和设备资产为事实来源；Atlas 当前维护监控侧观测资产库，后续通过接口同步事实资产，并与 Prometheus/DCGM 观测状态持续对账。', 'LXOPEasier room, rack and device records are authoritative. Atlas currently maintains the monitoring-observed inventory and will synchronize authoritative assets through an API for continuous reconciliation with Prometheus/DCGM observations.'), history: [tx('v0.2.0 · 明确 LXOPEasier 资产事实源；接口同步待接入', 'v0.2.0 · LXOPEasier established as source of truth; API sync pending'), tx('v0.1.2 · Target、身份增量与每日全量对账', 'v0.1.2 · target/identity incremental and daily full reconciliation'), tx('v0.1.1 · GPU 主机范围过滤与历史 UUID 恢复', 'v0.1.1 · GPU host filtering and historical UUID recovery'), tx('v0.1.0 · 90 节点 / 720 卡监控侧基线', 'v0.1.0 · 90-node / 720-GPU monitoring baseline')] },
    { id: 'quality', name: tx('监控数据质量发现', 'Monitoring Data Quality Detection'), version: 'v0.4.2', status: tx('双源质量基线', 'DUAL-SOURCE BASELINE'), desc: tx('识别当前 GPU 资产范围内的 Target 异常、指标缺失或陈旧、覆盖不足、身份冲突和采集链路问题；退休及非 GPU 节点不进入缺失统计。', 'Detect target failures, missing or stale metrics, coverage gaps, identity conflicts and collection-path issues within the current GPU asset scope; retired and non-GPU nodes are excluded.'), history: [tx('v0.4.2 · Target 问题严格限定当前 GPU 节点生命周期', 'v0.4.2 · target issues are strictly scoped to the current GPU-node lifecycle'), tx('v0.4.1 · 双源数值差异改为审计信息，不再生成数据质量问题', 'v0.4.1 · cross-source value differences became audit-only and no longer create data-quality issues'), tx('v0.4.0 · DCGM 与 gpu_exporter 规范化一致性观察', 'v0.4.0 · normalized DCGM/gpu_exporter consistency observations'), tx('v0.3.0 · 统一数据时效 API、SLA 边界和 8077 源时间标识', 'v0.3.0 · unified freshness API, SLA boundaries and source-time indicator on 8077')] },
    { id: 'health', name: tx('硬件健康评分', 'Hardware Health Scoring'), version: 'v1.1.2', status: tx('双源 GPU 基线', 'DUAL-SOURCE GPU BASELINE'), desc: tx('以 DCGM 为主、gpu_exporter 补充与降级备用；等价指标按优先级取值，数值差异不重复计分或降低置信度。', 'Use DCGM as primary and gpu_exporter as supplement/fallback; equivalent metrics follow source priority, and value differences neither duplicate scoring nor lower confidence.'), history: [tx('v1.1.2 · 双源差异仅审计，健康页精简为一行四项核心指标', 'v1.1.2 · source differences are audit-only and the health page now shows one row of four core metrics'), tx('v1.1.1 · 明确已评分平均分口径', 'v1.1.1 · clarified the scored-average scope'), tx('v1.1.0 · gpu_exporter 补充特征与 DCGM 优先降级备用', 'v1.1.0 · gpu_exporter supplements and DCGM-first fallback'), tx('v1.0.0 · GPU 六领域规则评分与 A–D 置信度', 'v1.0.0 · six-domain GPU rules and A–D confidence')] },
    { id: 'detection', name: tx('故障检测', 'Fault Detection'), version: 'v0.1.0', status: tx('规则基线', 'RULE BASELINE'), desc: tx('识别已经发生或正在发生的 ECC、XID、掉卡、不可用、温度和 PCIe 等硬件故障。AI + Skill 将基于事件与详情生成故障报告、根因分析和处理建议，辅助人工处置；该辅助能力目前仅完成实现路径设计。', 'Detect ECC, XID, dropout, unavailable, thermal and PCIe hardware faults that have occurred or are occurring. AI + Skill will generate fault reports, root-cause analysis and handling guidance from events and details to assist operators; this assisted capability currently has an implementation path only.'), history: [tx('v0.1.0 · Row Remap、温度、PCIe 与 XID 变化确定性规则', 'v0.1.0 · deterministic row-remap, thermal, PCIe and XID-change rules'), tx('路径定义 · AI + Skill 故障报告与处理建议（未开发）', 'PATH DEFINED · AI + Skill fault reports and handling guidance (not developed)')] },
    { id: 'feature', name: tx('统一特征目录', 'Unified Feature Catalog'), version: 'v1.1.0', status: tx('双源契约完成', 'DUAL-SOURCE CONTRACT'), desc: tx('为健康评分、PyOD、风险排序、监督预测和性能衰减提供版本化特征契约、双源优先级、单位规范化、型号能力、时间与缺失语义、质量、血缘和 owner。', 'Provide versioned feature contracts, source priority, unit normalization, model capabilities, time and missing semantics, quality, lineage and ownership for health scoring, PyOD, risk ranking, supervised prediction and degradation detection.'), history: [tx('v1.1.0 · 25 个规范健康特征、41 个源查询、来源血缘和回退语义', 'v1.1.0 · 25 canonical health features, 41 source queries, lineage and fallback semantics'), tx('v1.0.0 · 注册/读取 API、20 个健康核心特征和 snapshot 版本 manifest', 'v1.0.0 · registration/read APIs, 20 core health features and snapshot version manifests')] },
    { id: 'prediction', name: tx('硬件故障预警与预测', 'Hardware Early Warning & Failure Prediction'), version: 'v0.0.1', status: tx('特征底座就绪，模型未交付', 'FEATURE FOUNDATION READY'), desc: tx('承载故障发生前的风险预警与概率预测，覆盖 GPU，并按统一资产、特征和标签模型扩展至服务器、存储和网络硬件；当前仅完成统一特征底座，尚未交付 PyOD 或监督概率模型。', 'Provide pre-failure risk warnings and probability prediction for GPUs, extending through a common asset, feature and label model to server, storage and network hardware. The common feature foundation is ready; PyOD and supervised probability models are not yet delivered.'), history: [tx('v0.0.1 · Feature Catalog v1 可供异常检测、风险排序和监督训练消费', 'v0.0.1 · Feature Catalog v1 is consumable by anomaly detection, risk ranking and supervised training'), tx('v0.0.0 · 特征、标签、PyOD、监督模型与概率校准路线完成', 'v0.0.0 · feature, label, PyOD, supervised-model and probability-calibration roadmap')] },
    { id: 'degradation', name: tx('性能衰减识别', 'Performance Degradation Detection'), version: 'v0.0.0', status: tx('方案阶段', 'DESIGNED'), desc: tx('通过被动同类对比和维护窗口主动验证识别计算、显存、PCIe 与 NVLink 性能退化。', 'Detect compute, memory, PCIe and NVLink degradation through passive peer comparison and maintenance-window validation.'), history: [tx('v0.0.0 · 被动检测与主动验证安全门设计', 'v0.0.0 · passive detection and active-validation safety gates')] },
    { id: 'incident', name: tx('故障事件管理', 'Fault Incident Management'), version: 'v0.2.2', status: tx('稳定分页基线', 'STABLE PAGINATION BASELINE'), desc: tx('分层管理原始接收记录、Atlas 硬件事件与后续故障案例；接收记录和硬件事件均支持真实总数、服务端筛选和稳定 ID 游标分页。', 'Separate raw ingestion records, Atlas hardware events and future fault cases. Both ingestion records and hardware events provide real totals, server-side filtering and stable ID cursor pagination.'), history: [tx('v0.2.2 · 硬件事件稳定 ID 游标、服务端筛选与前端分页', 'v0.2.2 · stable hardware-event ID cursor, server-side filtering and UI pagination'), tx('v0.2.1 · 只读生产接收库、真实总数、游标分页与新鲜度', 'v0.2.1 · read-only production ingestion store, real totals, cursor pagination and freshness'), tx('v0.2.0 · 接收记录、硬件事件与故障案例分层', 'v0.2.0 · ingestion, hardware event and fault case layers'), tx('v0.1.0 · open / recovered 事件生命周期', 'v0.1.0 · open/recovered event lifecycle')] },
    { id: 'issue', name: tx('问题统计与处置', 'Issue Analytics & Resolution'), version: 'v0.2.2', status: tx('问题闭环基线', 'ISSUE WORKFLOW BASELINE'), desc: tx('统一统计当前资产范围内的平台问题、分类、自动检测状态和人工处置状态；退休及非 GPU 节点的历史 Target 不生成缺失问题。', 'Normalize platform issues within the current asset scope and their workflow states; historical targets on retired or non-GPU nodes do not create missing-data issues.'), history: [tx('v0.2.2 · 退休节点历史 Target 问题自动清除并退出活跃统计', 'v0.2.2 · historical target issues on retired nodes are automatically cleared and removed from active analytics'), tx('v0.2.1 · 双源数值差异退出列表、统计和训练数据', 'v0.2.1 · source-value differences left lists, analytics and training data'), tx('v0.2.0 · 双源一致性问题试运行', 'v0.2.0 · source-consistency issue trial'), tx('v0.1.0 · 五类检测源归一化、统计钻取、处置历史与训练数据导出', 'v0.1.0 · five-source normalization, analytics drill-down, resolution history and training export')] },
    { id: 'platform', name: tx('平台实例配置', 'Platform Instance Configuration'), version: 'v0.1.0', status: tx('配置基线完成', 'CONFIGURATION BASELINE'), desc: tx('通过平台概览维护实例名称、产品名称、产品副标题和环境标识，并统一驱动导航、面包屑、环境徽标与浏览器标题。', 'Manage the instance name, product name, product tagline and environment label from Platform Overview, consistently driving navigation, breadcrumbs, environment badges and the browser title.'), history: [tx('v0.1.0 · 数据库持久化、运行时读取、页面编辑与安全字段边界', 'v0.1.0 · database persistence, runtime reads, UI editing and public-field safety boundary')] },
    { id: 'validation', name: tx('维修验证闭环', 'Repair Validation Workflow'), version: 'v0.0.0', status: tx('方案阶段', 'DESIGNED'), desc: tx('记录人工维修反馈、根因、修复或更换结果，并通过识别、遥测、错误计数和性能复测验证重新上线。', 'Capture repair feedback, root cause and replacement results, then validate return to service through identity, telemetry, counters and performance checks.'), history: [tx('v0.0.0 · 状态机、维护窗口和验证门设计', 'v0.0.0 · state machine, maintenance window and validation gates')] },
  ];
  const milestones = [
    ['P0', tx('数据底座', 'Data Foundation'), tx('完成', 'COMPLETE')],
    ['P1', tx('GPU 健康', 'GPU Health'), tx('基线完成', 'BASELINE')],
    ['P2', tx('故障闭环', 'Incident Workflow'), tx('开发中', 'ACTIVE')],
    ['P2.5', tx('性能验证', 'Performance Validation'), tx('方案阶段', 'DESIGNED')],
    ['P3', tx('特征与异常检测', 'Features & Anomaly Detection'), tx('开发中', 'ACTIVE')],
    ['P4', tx('硬件预警与预测', 'Hardware Warning & Prediction'), tx('规划', 'PLANNED')],
  ];
  const selectedModule = modules.find(module => module.id === moduleDetailID) || null;
  return <>
  <div className="grid">
    <Card className="span-12 product-intro"><div><span>{platformConfig.product_name}</span><h2>Infrastructure Hardware Reliability Workbench</h2><p>{tx('ATLAS 是面向 GPU 集群并可扩展至服务器、存储和网络基础设施的硬件可靠性工作台，提供资产对账、监控数据质量发现、硬件健康评分、故障检测、问题统计与处置、硬件故障预警与预测、性能衰减识别、故障事件管理以及维修验证闭环。', 'ATLAS is a hardware reliability workbench for GPU clusters, extensible to server, storage and network infrastructure. It provides asset reconciliation, monitoring data quality detection, hardware health scoring, fault detection, issue analytics and resolution, hardware early warning and failure prediction, performance degradation analysis, incident management and repair validation workflows.')}</p></div><Badge value="PLATFORM / v0.5.3" kind="info" /></Card>
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
        <label><span>{tx('环境标识', 'Environment label')}<small>environment · 40</small></span><input maxLength={40} value={draft.environment} onChange={event => update('environment', event.target.value)} placeholder="TEST / 8077" /></label>
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

function Drawer({ tx, item, report, lang, close }: { tx: Tx; item: Ingestion; report: Report | null; lang: string; close: () => void }) {
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>INCIDENT / #{item.id}</span><h2>{item.message}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={item.level} kind={tone(item.level)} /><span>{item.host}</span><time>{time(item.created_at, lang)}</time></div><div className="detail-grid">{[['GPU UUID', item.labels?.UUID || item.labels?.uuid], ['PCI BUS ID', item.labels?.pci_bus_id], ['DEVICE', item.labels?.device || item.labels?.gpu], ['MODEL', item.labels?.modelName || item.labels?.model]].map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1] || '—'}</b></div>)}</div><section><CardHead code="ANALYSIS" title={tx('分析', 'Analysis')} action={report?.confidence != null ? <Badge value={`${Math.round(report.confidence * 100)}%`} kind="info" /> : undefined} />{report ? <div className="report"><p>{report.summary}</p>{report.probable_causes?.length ? <><b>{tx('可能原因', 'Probable causes')}</b><ul>{report.probable_causes.map(x => <li key={x}>{x}</li>)}</ul></> : null}{report.recommended_actions?.length ? <><b>{tx('处理建议', 'Actions')}</b><ol>{report.recommended_actions.map(x => <li key={x}>{x}</li>)}</ol></> : null}<small>{report.model} · DRAFT</small></div> : <p className="muted">{tx('无分析结果', 'No analysis result')}</p>}</section><section><CardHead code="DETAILS" title={tx('标签', 'Labels')} /><div className="labels">{Object.entries(item.labels || {}).map(([k, v]) => <div key={k}><small>{k}</small><b>{v}</b></div>)}</div></section><section><CardHead code="RAW" title="Payload" /><pre>{item.raw_payload || '—'}</pre></section></div></motion.aside></motion.div>;
}
