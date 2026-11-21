import { lazy, Suspense, useEffect, useMemo, useState } from 'react';
import { AlertTriangle, ChevronLeft, ChevronRight, HardDrive, Loader2, MemoryStick, RefreshCw, Search, ServerCrash, ShieldAlert } from 'lucide-react';
import './HardwareFleet.css';

const HardwareEvidence = lazy(() => import('./HardwareEvidence'));

type Tx = (zh: string, en: string) => string;
type Level = 'critical' | 'warning' | 'attention' | 'clear';
type Disk = {
  ip: string; host: string; kind: string; dev: string; proto: string; model: string; serial: string; fw: string;
  cap_gb: number | null; poh: number | null; temp: number | null; passed: string; wear: number | null; spare: number | null;
  crit: number | null; media: number | null; errlog: number | null; realloc: number | null; unc: number | null; timeout: number | null;
  pending: number | null; offunc: number | null; crc: number | null; written_tb: number | null; delta: Record<string, number>;
  raid?: { slot: string; state: string; media_err: number | null; other_err: number | null; pred_fail: number | null };
  hits: string[]; level: Level; life_days?: number;
};
type Memory = {
  ip: string; host: string; kind: string; dimms: number; mem_gb: number; hw_corrupted_kb: number | null; edac: number; mce: number; mem_offline: number;
  ce_7d?: number; ce_30d?: number; ue_30d?: number; ue_total?: number; ue_last?: string; ce_last?: string; disabled_30d?: number; limit_30d?: number; locs?: string[];
  hits: string[]; level: Level;
};
type NodeRow = { ip: string; host: string; kind: string; status: string; flags: string[]; followup: string[]; node_exporter?: boolean | null; ipmi?: boolean | null };
type LevelCount = Record<Level, number>;
type Fleet = {
  schema: string; rule_version: string; day: string; previous_day: string | null; collected_at: string | null; generated_at: string;
  coverage: { nodes_total: number; collected: number; by_status: Record<string, number>; by_kind: Record<string, Record<string, number>>; flags: Record<string, number>; followup: Record<string, number>; smart_nodes: number; smart_disks: number; collected_without_smart: number; failed_not_in_vm: number | null };
  summary: { disk: { total: number; by_level: LevelCount; by_rule: Record<string, number>; by_protocol: Record<string, number>; with_delta_baseline: number; models: [string, number][] }; memory: { nodes: number; by_level: LevelCount; by_rule: Record<string, number>; dimms: number } };
  disks: Disk[]; memory: Memory[]; nodes: NodeRow[];
};

const rank: Record<Level, number> = { critical: 3, warning: 2, attention: 1, clear: 0 };
const statusOrder = ['ok', 'partial_timeout', 'ssh_auth_failed', 'ssh_unreachable', 'incomplete_rc255'];

function ruleText(code: string, tx: Tx): string {
  const map: Record<string, [string, string]> = {
    smart_overall_failed: ['SMART 总体判定失败', 'SMART overall failed'], smart_read_error: ['SMART 读取失败', 'SMART unreadable'],
    nvme_critical_warning: ['NVMe 严重告警位', 'NVMe critical warning'], pending_sectors: ['待映射扇区', 'Pending sectors'],
    offline_uncorrectable: ['离线不可纠正扇区', 'Offline uncorrectable'], reported_uncorrectable: ['上报不可纠正错误', 'Reported uncorrectable'],
    nvme_spare_low: ['NVMe 备用块 ≤10%', 'NVMe spare ≤10%'], media_errors_new: ['介质错误新增', 'New media errors'],
    raid_predictive_failure: ['RAID 预测失败', 'RAID predictive failure'], reallocated_new: ['重映射扇区新增', 'New reallocations'],
    wear_ge_80: ['寿命消耗 ≥80%', 'Wear ≥80%'], media_errors_present: ['存在介质错误（未增长）', 'Media errors present (stable)'],
    scsi_defects: ['SCSI 缺陷/不可纠正', 'SCSI defects'], raid_media_error: ['RAID 介质错误', 'RAID media errors'],
    reallocated_present: ['存在重映射扇区（未增长）', 'Reallocations present (stable)'], wear_ge_50: ['寿命消耗 ≥50%', 'Wear ≥50%'],
    crc_new: ['链路 CRC 新增', 'New link CRC errors'], cmd_timeout_new: ['命令超时新增', 'New command timeouts'],
    nvme_errlog_new: ['NVMe 错误日志新增', 'New NVMe error-log entries'], temperature_ge_60: ['温度 ≥60°C', 'Temperature ≥60°C'],
    sel_ue_30d: ['30 天内不可纠正 ECC', 'Uncorrectable ECC in 30d'], hardware_corrupted: ['内核已隔离损坏内存页', 'Kernel isolated corrupted pages'],
    kernel_page_offline: ['内核内存页下线', 'Kernel page offline'], dimm_disabled_30d: ['30 天内内存条被禁用', 'DIMM disabled in 30d'],
    sel_ce_storm_7d: ['7 天可纠正 ECC ≥100', '≥100 corrected ECC in 7d'], sel_ce_limit_reached: ['可纠正 ECC 记录达上限', 'Corrected-ECC log limit reached'],
    kernel_edac_mce: ['内核 EDAC/MCE 日志', 'Kernel EDAC/MCE lines'], sel_ce_30d: ['30 天内可纠正 ECC', 'Corrected ECC in 30d'],
    sel_ue_history: ['历史不可纠正 ECC（>30 天）', 'Uncorrectable ECC >30d ago'],
  };
  const v = map[code];
  return v ? tx(v[0], v[1]) : code;
}
function statusText(code: string, tx: Tx): string {
  const map: Record<string, [string, string]> = {
    ok: ['已采集', 'Collected'], partial_timeout: ['部分采集（超时）', 'Partial (timeout)'], ssh_auth_failed: ['SSH 认证失败', 'SSH auth failed'],
    ssh_unreachable: ['SSH 不可达', 'SSH unreachable'], incomplete_rc255: ['连接中断', 'Connection reset'], missing: ['无记录', 'No record'],
  };
  const v = map[code];
  return v ? tx(v[0], v[1]) : code;
}
function followText(code: string, tx: Tx): string {
  const map: Record<string, [string, string]> = {
    collect_failed: ['采集失败', 'Collection failed'], collect_partial: ['部分采集', 'Partial collection'], device_access_hang: ['设备访问卡住（lsblk/lspci/storcli）', 'Device access hang'],
    sel_full: ['SEL 已满', 'SEL full'], sel_unreadable: ['SEL 无法读取', 'SEL unreadable'], smartmontools_missing: ['未安装 smartctl', 'smartctl missing'],
    smartmontools_pkg_broken: ['smartmontools 安装不完整', 'smartmontools half-installed'], smartd_active: ['节点自带 smartd 在运行', 'smartd already running'],
  };
  const v = map[code];
  return v ? tx(v[0], v[1]) : code;
}
function LevelTag({ level, tx }: { level: Level; tx: Tx }) {
  const label = { critical: tx('严重', 'Critical'), warning: tx('警告', 'Warning'), attention: tx('关注', 'Attention'), clear: tx('未命中', 'Clear') }[level];
  return <span className={`hf-level hf-${level}`}><i />{label}</span>;
}
const fmt = (v: number | null | undefined, unit = '') => (v === null || v === undefined ? '—' : `${v.toLocaleString()}${unit}`);
function lifeText(days: number | undefined, tx: Tx) {
  if (days === undefined) return '—';
  if (days > 3650) return tx('>10 年', '>10 y');
  if (days > 365) return tx(`${(days / 365).toFixed(1)} 年`, `${(days / 365).toFixed(1)} y`);
  return tx(`${days} 天`, `${days} d`);
}
function delta(d: Disk) {
  const parts = Object.entries(d.delta).filter(([k, v]) => k !== 'wear' && v !== 0).map(([k, v]) => `${k} ${v > 0 ? '+' : ''}${v}`);
  return parts.length ? parts.join(' · ') : '';
}

function Pager({ total, page, size, setPage }: { total: number; page: number; size: number; setPage: (n: number) => void }) {
  const last = Math.max(0, Math.ceil(total / size) - 1);
  return <div className="hf-pager"><span>{total ? page * size + 1 : 0}–{Math.min(total, (page + 1) * size)} / {total}</span><button className="icon-btn" disabled={page === 0} onClick={() => setPage(page - 1)} aria-label="previous"><ChevronLeft size={15} /></button><button className="icon-btn" disabled={page >= last} onClick={() => setPage(page + 1)} aria-label="next"><ChevronRight size={15} /></button></div>;
}

export default function HardwareHealth({ tx, lang }: { tx: Tx; lang: string }) {
  const [fleet, setFleet] = useState<Fleet | null>(null);
  const [days, setDays] = useState<string[]>([]);
  const [day, setDay] = useState('');
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const requestKey = `${day}|${reload}`;
  const [doneKey, setDoneKey] = useState('');
  const loading = doneKey !== requestKey;
  const [tab, setTab] = useState<'risk' | 'disk' | 'memory' | 'coverage' | 'evidence'>('risk');

  useEffect(() => {
    const controller = new AbortController();
    fetch(`/api/v1/hardware/fleet${day ? `?day=${day}` : ''}`, { signal: controller.signal })
      .then(async response => { const body = await response.json().catch(() => null); if (!response.ok) throw new Error(body?.error?.message || `HTTP ${response.status}`); return body; })
      .then(body => { if (body?.data?.schema !== 'node-hw-fleet-v1') throw new Error('unsupported snapshot schema'); setFleet(body.data); setDays(body.available_days || []); setError(''); })
      .catch(cause => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)); })
      .finally(() => { if (!controller.signal.aborted) setDoneKey(requestKey); });
    return () => controller.abort();
  }, [day, requestKey]);

  const tabs = [
    { id: 'risk', label: tx('待复核', 'To review'), count: fleet ? fleet.disks.filter(d => d.level !== 'clear').length + fleet.memory.filter(m => m.level !== 'clear').length : undefined },
    { id: 'disk', label: tx('硬盘', 'Disks'), count: fleet?.summary.disk.total },
    { id: 'memory', label: tx('内存', 'Memory'), count: fleet?.summary.memory.nodes },
    { id: 'coverage', label: tx('采集覆盖', 'Coverage'), count: fleet ? fleet.coverage.nodes_total - fleet.coverage.collected : undefined },
    { id: 'evidence', label: tx('单盘证据（旧）', 'Disk evidence (legacy)'), count: undefined },
  ] as const;

  return <div className="hf-page" aria-busy={loading}>
    <div className="hf-source">
      <p>{fleet ? tx(`${fleet.day} 每日只读采集 · 规则 ${fleet.rule_version}${fleet.previous_day ? ` · 增量对比 ${fleet.previous_day}` : ' · 无前一日基线'}`, `${fleet.day} daily read-only collection · rules ${fleet.rule_version}${fleet.previous_day ? ` · delta vs ${fleet.previous_day}` : ' · no previous-day baseline'}`) : tx('读取每日只读采集结果', 'Loading daily read-only collection')}</p>
      <div className="hf-source-actions">
        {days.length > 1 && <select aria-label={tx('采集日期', 'Collection day')} value={day || fleet?.day || ''} onChange={event => setDay(event.target.value === days[0] ? '' : event.target.value)}>{days.map(value => <option key={value} value={value}>{value}</option>)}</select>}
        <button className="icon-btn" onClick={() => setReload(value => value + 1)} title={tx('重新读取', 'Reload')} aria-label={tx('重新读取', 'Reload')}>{loading ? <Loader2 size={15} className="spin" /> : <RefreshCw size={15} />}</button>
      </div>
    </div>
    {error && <div className="hf-error" role="alert"><AlertTriangle size={16} /><div><b>{tx('未能读取机队硬件快照', 'Fleet hardware snapshot unavailable')}</b><span>{error}</span><small>{tx('快照由每日 01:40 采集生成；仍可在“单盘证据（旧）”查看导入报告。', 'Generated by the 01:40 daily collection; imported reports remain under “Disk evidence (legacy)”.')}</small></div></div>}
    {fleet && <Overview fleet={fleet} tx={tx} go={setTab} />}
    <div className="hf-tabs" role="tablist">{tabs.map(item => <button key={item.id} role="tab" aria-selected={tab === item.id} onClick={() => setTab(item.id)}>{item.label}{item.count !== undefined && <span>{item.count}</span>}</button>)}</div>
    {tab === 'evidence' ? <Suspense fallback={<div className="hf-empty"><Loader2 size={18} className="spin" /></div>}><HardwareEvidence tx={tx} lang={lang} /></Suspense>
      : !fleet ? <div className="hf-empty">{loading ? <Loader2 size={18} className="spin" /> : tx('暂无数据', 'No data')}</div>
      : tab === 'risk' ? <RiskList fleet={fleet} tx={tx} />
      : tab === 'disk' ? <DiskTable fleet={fleet} tx={tx} />
      : tab === 'memory' ? <MemoryTable fleet={fleet} tx={tx} />
      : <Coverage fleet={fleet} tx={tx} />}
  </div>;
}

function Overview({ fleet, tx, go }: { fleet: Fleet; tx: Tx; go: (tab: 'risk' | 'disk' | 'memory' | 'coverage') => void }) {
  const c = fleet.coverage;
  const segments = statusOrder.map(id => ({ id, n: c.by_status[id] || 0 })).concat(Object.entries(c.by_status).filter(([k]) => !statusOrder.includes(k)).map(([id, n]) => ({ id, n })) ).filter(s => s.n > 0);
  const d = fleet.summary.disk.by_level, m = fleet.summary.memory.by_level;
  const wearValues = fleet.disks.map(x => x.wear).filter((x): x is number => x !== null);
  const buckets = [[0, 10], [10, 30], [30, 50], [50, 80], [80, 101]].map(([lo, hi]) => wearValues.filter(v => v >= lo && v < hi).length);
  return <section className="hf-overview">
    <div className="hf-coverage">
      <header><h2>{tx(`${c.collected} / ${c.nodes_total} 个节点已采集`, `${c.collected} of ${c.nodes_total} nodes collected`)}</h2><button className="hf-link" onClick={() => go('coverage')}>{tx('查看未采集节点', 'See uncollected nodes')}</button></header>
      <div className="hf-bar" role="img" aria-label={segments.map(s => `${statusText(s.id, tx)} ${s.n}`).join(', ')}>{segments.map(s => <span key={s.id} className={`hf-seg hf-seg-${s.id}`} style={{ flexGrow: s.n }} title={`${statusText(s.id, tx)} ${s.n}`} />)}</div>
      <ul className="hf-legend">{segments.map(s => <li key={s.id}><i className={`hf-seg-${s.id}`} />{statusText(s.id, tx)}<b>{s.n}</b></li>)}</ul>
      <p>{tx(`SMART 覆盖 ${c.smart_nodes} 个节点 ${c.smart_disks} 块盘；已连上但无 SMART 的节点 ${c.collected_without_smart} 个（无 smartctl、超时或仅有 RAID 虚拟盘）。`, `SMART covers ${c.smart_disks} disks on ${c.smart_nodes} nodes; ${c.collected_without_smart} reachable nodes returned no SMART (no smartctl, timeout or RAID-only).`)}{c.failed_not_in_vm !== null && tx(` 未采集节点中 ${c.failed_not_in_vm} 个也不在 VM 监控内。`, ` ${c.failed_not_in_vm} uncollected nodes are also absent from VM monitoring.`)}</p>
    </div>
    <div className="hf-health">
      <button onClick={() => go('disk')}><HardDrive size={17} /><span>{tx('硬盘', 'Disks')}</span><strong>{fleet.summary.disk.total.toLocaleString()}</strong><LevelRow counts={d} tx={tx} /><small>{tx('寿命消耗分布', 'Wear distribution')}</small><div className="hf-wear">{buckets.map((n, i) => <span key={i} title={`${['0–10', '10–30', '30–50', '50–80', '≥80'][i]}%: ${n}`}><i style={{ height: `${Math.max(2, (n / Math.max(1, ...buckets)) * 100)}%` }} className={i >= 3 ? 'hot' : ''} /><em>{['<10', '10', '30', '50', '80'][i]}</em></span>)}</div></button>
      <button onClick={() => go('memory')}><MemoryStick size={17} /><span>{tx('内存（按节点）', 'Memory (per node)')}</span><strong>{fleet.summary.memory.nodes.toLocaleString()}</strong><LevelRow counts={m} tx={tx} /><small>{tx(`${fleet.summary.memory.dimms.toLocaleString()} 根内存条；来源 BMC SEL、内核日志、HardwareCorrupted`, `${fleet.summary.memory.dimms.toLocaleString()} DIMMs; sources BMC SEL, kernel log, HardwareCorrupted`)}</small></button>
    </div>
  </section>;
}

function LevelRow({ counts, tx }: { counts: LevelCount; tx: Tx }) {
  return <div className="hf-levelrow">{(['critical', 'warning', 'attention'] as Level[]).map(level => <span key={level} className={counts[level] ? `hf-${level}` : 'hf-zero'}><b>{counts[level]}</b>{({ critical: tx('严重', 'critical'), warning: tx('警告', 'warning'), attention: tx('关注', 'attention'), clear: '' })[level]}</span>)}</div>;
}

type RiskItem = { key: string; type: 'disk' | 'memory'; level: Level; ip: string; host: string; what: string; hits: string[]; detail: string };
function RiskList({ fleet, tx }: { fleet: Fleet; tx: Tx }) {
  const [level, setLevel] = useState('');
  const [type, setType] = useState('');
  const items = useMemo<RiskItem[]>(() => [
    ...fleet.memory.filter(m => m.level !== 'clear').map(m => ({ key: `m-${m.ip}`, type: 'memory' as const, level: m.level, ip: m.ip, host: m.host, what: tx(`${m.dimms} 根 · ${m.mem_gb} GB`, `${m.dimms} DIMMs · ${m.mem_gb} GB`), hits: m.hits, detail: [m.ue_30d ? `UE30d ${m.ue_30d}` : '', m.ue_total ? `UE ${m.ue_total}${m.ue_last ? ` (${m.ue_last})` : ''}` : '', m.ce_7d ? `CE7d ${m.ce_7d}` : '', m.ce_30d ? `CE30d ${m.ce_30d}` : '', m.hw_corrupted_kb ? `HardwareCorrupted ${m.hw_corrupted_kb} kB` : '', m.edac ? `EDAC ${m.edac}` : '', m.mce ? `MCE ${m.mce}` : '', m.locs?.length ? m.locs.join(', ') : ''].filter(Boolean).join(' · ') })),
    ...fleet.disks.filter(d => d.level !== 'clear').map(d => ({ key: `d-${d.ip}-${d.serial}-${d.dev}`, type: 'disk' as const, level: d.level, ip: d.ip, host: d.host, what: `${d.model} · ${d.serial || d.dev}`, hits: d.hits, detail: [d.realloc ? `realloc ${d.realloc}` : '', d.media ? `media ${d.media}` : '', d.wear !== null ? `wear ${d.wear}%` : '', delta(d)].filter(Boolean).join(' · ') })),
  ].filter(i => (!level || i.level === level) && (!type || i.type === type)).sort((a, b) => rank[b.level] - rank[a.level] || a.type.localeCompare(b.type) || a.ip.localeCompare(b.ip, undefined, { numeric: true })), [fleet, level, type, tx]);
  return <section className="hf-panel">
    <p className="hf-note">{tx('状态与劣化规则命中，不是故障概率；不会触发任何处置。“未增长”项为历史累计值，需与次日数据对比确认是否在恶化。', 'Status and degradation rule hits, not failure probability; no action is taken. “Stable” items are historical totals — compare with the next day to see whether they grow.')}</p>
    <div className="hf-toolbar"><select value={type} onChange={e => setType(e.target.value)} aria-label={tx('类型', 'Type')}><option value="">{tx('硬盘与内存', 'Disks and memory')}</option><option value="memory">{tx('内存', 'Memory')}</option><option value="disk">{tx('硬盘', 'Disks')}</option></select><select value={level} onChange={e => setLevel(e.target.value)} aria-label={tx('等级', 'Level')}><option value="">{tx('全部等级', 'All levels')}</option><option value="critical">{tx('严重', 'Critical')}</option><option value="warning">{tx('警告', 'Warning')}</option><option value="attention">{tx('关注', 'Attention')}</option></select><span>{items.length}</span></div>
    {items.length === 0 ? <div className="hf-empty"><ShieldAlert size={20} />{tx('没有命中规则的项', 'No rule hits')}</div> :
    <div className="hf-scroll"><table className="hf-table"><thead><tr><th>{tx('等级', 'Level')}</th><th>{tx('节点', 'Node')}</th><th>{tx('部件', 'Component')}</th><th>{tx('命中规则', 'Rule hits')}</th><th>{tx('证据', 'Evidence')}</th></tr></thead><tbody>{items.map(i => <tr key={i.key}><td><LevelTag level={i.level} tx={tx} /></td><td><b>{i.ip}</b><small>{i.host}</small></td><td>{i.type === 'disk' ? <HardDrive size={13} /> : <MemoryStick size={13} />} {i.what}</td><td>{i.hits.map(h => <span key={h} className="hf-rule">{ruleText(h, tx)}</span>)}</td><td><code>{i.detail || '—'}</code></td></tr>)}</tbody></table></div>}
  </section>;
}

function DiskTable({ fleet, tx }: { fleet: Fleet; tx: Tx }) {
  const [q, setQ] = useState('');
  const [proto, setProto] = useState('');
  const [level, setLevel] = useState('');
  const [sort, setSort] = useState('risk');
  const [page, setPage] = useState(0);
  const size = 50;
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const list = fleet.disks.filter(d => (!proto || d.proto === proto) && (!level || d.level === level) && (!needle || [d.ip, d.host, d.model, d.serial, d.fw].some(v => v.toLowerCase().includes(needle))));
    const key: Record<string, (d: Disk) => number> = { risk: d => rank[d.level] * 1000 + (d.wear || 0), wear: d => d.wear ?? -1, life: d => -(d.life_days ?? 1e9), temp: d => d.temp ?? -1, poh: d => d.poh ?? -1 };
    return list.sort((a, b) => key[sort](b) - key[sort](a) || a.ip.localeCompare(b.ip, undefined, { numeric: true }));
  }, [fleet, q, proto, level, sort]);
  const visible = rows.slice(page * size, (page + 1) * size);
  return <section className="hf-panel">
    <p className="hf-note">{tx('剩余寿命按该盘至今的平均磨损速度线性外推，仅适用于 SSD，且未经故障数据验证；HDD 与无磨损指标的盘不估计。', 'Remaining life extrapolates each SSD’s lifetime-average wear rate; unvalidated against failures. HDDs and drives without a wear indicator are not estimated.')}</p>
    <div className="hf-toolbar"><label className="hf-search"><Search size={14} /><input value={q} onChange={e => { setQ(e.target.value); setPage(0); }} placeholder={tx('节点、型号、序列号', 'Node, model, serial')} /></label>
      <select value={proto} onChange={e => { setProto(e.target.value); setPage(0); }} aria-label={tx('协议', 'Protocol')}><option value="">{tx('全部协议', 'All protocols')}</option>{Object.entries(fleet.summary.disk.by_protocol).map(([p, n]) => <option key={p} value={p}>{p} ({n})</option>)}</select>
      <select value={level} onChange={e => { setLevel(e.target.value); setPage(0); }} aria-label={tx('等级', 'Level')}><option value="">{tx('全部等级', 'All levels')}</option><option value="critical">{tx('严重', 'Critical')}</option><option value="warning">{tx('警告', 'Warning')}</option><option value="attention">{tx('关注', 'Attention')}</option><option value="clear">{tx('未命中', 'Clear')}</option></select>
      <select value={sort} onChange={e => { setSort(e.target.value); setPage(0); }} aria-label={tx('排序', 'Sort')}><option value="risk">{tx('风险优先', 'Risk first')}</option><option value="wear">{tx('寿命消耗最高', 'Highest wear')}</option><option value="life">{tx('剩余寿命最短', 'Shortest remaining life')}</option><option value="temp">{tx('温度最高', 'Hottest')}</option><option value="poh">{tx('通电时间最长', 'Longest power-on')}</option></select>
    </div>
    <div className="hf-scroll"><table className="hf-table"><thead><tr><th>{tx('等级', 'Level')}</th><th>{tx('节点', 'Node')}</th><th>{tx('型号 / 序列号', 'Model / serial')}</th><th className="num">{tx('容量', 'Size')}</th><th className="num">{tx('通电', 'Power-on')}</th><th className="num">{tx('温度', 'Temp')}</th><th className="num">{tx('寿命消耗', 'Wear')}</th><th className="num">{tx('估计剩余', 'Est. left')}</th><th>{tx('错误计数 / 日增量', 'Error counters / daily delta')}</th></tr></thead>
      <tbody>{visible.map(d => <tr key={`${d.ip}-${d.serial}-${d.dev}`}><td><LevelTag level={d.level} tx={tx} />{d.hits.map(h => <small key={h}>{ruleText(h, tx)}</small>)}</td><td><b>{d.ip}</b><small>{d.host}</small></td><td><b>{d.model || '—'}</b><small>{d.proto} · {d.serial || d.dev}{d.raid ? ` · RAID ${d.raid.state}` : ''}</small></td><td className="num">{d.cap_gb ? (d.cap_gb >= 1000 ? `${(d.cap_gb / 1000).toFixed(1)} TB` : `${d.cap_gb} GB`) : '—'}</td><td className="num">{d.poh !== null ? tx(`${Math.round(d.poh / 24)} 天`, `${Math.round(d.poh / 24)} d`) : '—'}</td><td className="num">{fmt(d.temp, '°C')}</td><td className="num"><span className="hf-wearcell"><i style={{ width: `${Math.min(100, d.wear ?? 0)}%` }} className={(d.wear ?? 0) >= 50 ? 'hot' : ''} />{fmt(d.wear, '%')}</span></td><td className="num">{lifeText(d.life_days, tx)}</td><td><code>{[d.proto === 'NVMe' ? `media ${fmt(d.media)} · spare ${fmt(d.spare, '%')} · errlog ${fmt(d.errlog)}` : `realloc ${fmt(d.realloc)} · pending ${fmt(d.pending)} · crc ${fmt(d.crc)}`, delta(d) && `Δ ${delta(d)}`].filter(Boolean).join('  ')}</code></td></tr>)}</tbody></table></div>
    <Pager total={rows.length} page={page} size={size} setPage={setPage} />
  </section>;
}

function MemoryTable({ fleet, tx }: { fleet: Fleet; tx: Tx }) {
  const [q, setQ] = useState('');
  const [level, setLevel] = useState('');
  const [page, setPage] = useState(0);
  const size = 50;
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return fleet.memory.filter(m => (!level || m.level === level) && (!needle || [m.ip, m.host].some(v => v.toLowerCase().includes(needle))))
      .sort((a, b) => rank[b.level] - rank[a.level] || (b.ce_30d || 0) - (a.ce_30d || 0) || a.ip.localeCompare(b.ip, undefined, { numeric: true }));
  }, [fleet, q, level]);
  const visible = rows.slice(page * size, (page + 1) * size);
  return <section className="hf-panel">
    <p className="hf-note">{tx('多数机型为固件优先（firmware-first），操作系统 EDAC 看不到大部分可纠正错误，因此以 BMC SEL 为主；SEL 已满的 96 个 GPU 节点新事件可能缺失。定位到具体内存条仅在 SEL 提供槽位时可用。', 'Most platforms are firmware-first, so OS EDAC misses most corrected errors; BMC SEL is the main source. 96 GPU nodes have a full SEL and may miss new events. DIMM location only when the SEL names a slot.')}</p>
    <div className="hf-toolbar"><label className="hf-search"><Search size={14} /><input value={q} onChange={e => { setQ(e.target.value); setPage(0); }} placeholder={tx('节点 IP 或主机名', 'Node IP or hostname')} /></label><select value={level} onChange={e => { setLevel(e.target.value); setPage(0); }} aria-label={tx('等级', 'Level')}><option value="">{tx('全部等级', 'All levels')}</option><option value="critical">{tx('严重', 'Critical')}</option><option value="warning">{tx('警告', 'Warning')}</option><option value="attention">{tx('关注', 'Attention')}</option><option value="clear">{tx('未命中', 'Clear')}</option></select></div>
    <div className="hf-scroll"><table className="hf-table"><thead><tr><th>{tx('等级', 'Level')}</th><th>{tx('节点', 'Node')}</th><th className="num">{tx('内存', 'Memory')}</th><th className="num">{tx('CE 7 天', 'CE 7d')}</th><th className="num">{tx('CE 30 天', 'CE 30d')}</th><th className="num">{tx('UE 30 天 / 累计', 'UE 30d / total')}</th><th className="num">HardwareCorrupted</th><th className="num">{tx('内核 EDAC / MCE', 'Kernel EDAC / MCE')}</th><th>{tx('涉及槽位', 'Slots named')}</th></tr></thead>
      <tbody>{visible.map(m => <tr key={m.ip}><td><LevelTag level={m.level} tx={tx} />{m.hits.map(h => <small key={h}>{ruleText(h, tx)}</small>)}</td><td><b>{m.ip}</b><small>{m.host}</small></td><td className="num">{m.dimms ? `${m.dimms} × · ${m.mem_gb} GB` : '—'}</td><td className="num">{fmt(m.ce_7d ?? 0)}</td><td className="num">{fmt(m.ce_30d ?? 0)}</td><td className="num">{m.ue_30d || 0} / {m.ue_total || 0}{m.ue_last && <small>{m.ue_last}</small>}</td><td className="num">{m.hw_corrupted_kb === null ? '—' : `${m.hw_corrupted_kb} kB`}</td><td className="num">{m.edac} / {m.mce}{m.mem_offline ? <small>offline {m.mem_offline}</small> : null}</td><td>{m.locs?.length ? <code>{m.locs.join(', ')}</code> : '—'}</td></tr>)}</tbody></table></div>
    <Pager total={rows.length} page={page} size={size} setPage={setPage} />
  </section>;
}

function Coverage({ fleet, tx }: { fleet: Fleet; tx: Tx }) {
  const [status, setStatus] = useState('failed');
  const [page, setPage] = useState(0);
  const size = 50;
  const rows = fleet.nodes.filter(n => status === 'failed' ? !(n.status === 'ok' || n.status.startsWith('partial')) : status === 'issues' ? n.followup.length > 0 : n.status === status);
  const vmKnown = fleet.nodes.some(n => n.node_exporter !== undefined);
  return <section className="hf-panel">
    <div className="hf-follow">{Object.entries(fleet.coverage.followup).sort((a, b) => b[1] - a[1]).map(([k, n]) => <div key={k}><b>{n}</b><span>{followText(k, tx)}</span></div>)}</div>
    <div className="hf-toolbar"><select value={status} onChange={e => { setStatus(e.target.value); setPage(0); }} aria-label={tx('节点筛选', 'Node filter')}><option value="failed">{tx('未采集节点', 'Uncollected nodes')}</option><option value="issues">{tx('有待处理项的节点', 'Nodes with follow-ups')}</option>{Object.keys(fleet.coverage.by_status).map(s => <option key={s} value={s}>{statusText(s, tx)} ({fleet.coverage.by_status[s]})</option>)}</select><span>{rows.length}</span></div>
    {rows.length === 0 ? <div className="hf-empty"><ServerCrash size={20} />{tx('没有节点', 'No nodes')}</div> : <>
    <div className="hf-scroll"><table className="hf-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>{tx('类型', 'Kind')}</th><th>{tx('采集状态', 'Collection')}</th>{vmKnown && <th>{tx('VM 监控', 'In VM')}</th>}<th>{tx('待处理 / 缺项', 'Follow-ups / gaps')}</th></tr></thead>
      <tbody>{rows.slice(page * size, (page + 1) * size).map(n => <tr key={n.ip}><td><b>{n.ip}</b><small>{n.host}</small></td><td>{n.kind === 'gpu_node' ? 'GPU' : 'CPU'}</td><td><span className={`hf-st hf-st-${n.status}`}>{statusText(n.status, tx)}</span></td>{vmKnown && <td>{n.node_exporter === true ? tx('在线', 'Up') : n.node_exporter === false ? tx('在册但 down', 'Down') : n.ipmi != null ? tx('仅 IPMI', 'IPMI only') : <span className="hf-critical">{tx('不在监控', 'Not monitored')}</span>}</td>}<td>{n.followup.map(f => <span key={f} className="hf-rule">{followText(f, tx)}</span>)}{n.flags.length > 0 && <small><code>{n.flags.join(', ')}</code></small>}</td></tr>)}</tbody></table></div>
    <Pager total={rows.length} page={page} size={size} setPage={setPage} /></>}
  </section>;
}
