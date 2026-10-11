import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { AlertTriangle, ArrowDownWideNarrow, CheckCircle2, ChevronLeft, ChevronRight, Download, FileCheck2, HardDrive, Loader2, MemoryStick, Network, Search, ShieldAlert, Trash2, Upload, X } from 'lucide-react';
import { MAX_BUNDLE_BYTES, diskDimensionForRule, levels, parseHealthBundle, selectRows, type HealthLevel, type HealthRow, type ImportedHealthReport } from './hardwareHealthReport';
import './HardwareHealth.css';

type Tx = (zh: string, en: string) => string;
function navigateTabs(event: KeyboardEvent<HTMLButtonElement>) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  const buttons = Array.from(event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]') ?? []);
  const index = buttons.indexOf(event.currentTarget);
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length;
  event.preventDefault(); buttons[next]?.focus(); buttons[next]?.click();
}
function ruleTitle(rule: string, tx: Tx): string {
  const names: Record<string, string> = {
    ata_current_prefailure_threshold: tx('当前预失效属性达到厂商阈值', 'Current pre-failure attribute at manufacturer threshold'),
    ata_current_usage_threshold: tx('当前使用属性达到厂商阈值', 'Current usage attribute at manufacturer threshold'),
    ata_past_threshold_failure: tx('历史属性阈值越界', 'Historical attribute threshold failure'),
    ata_pending_sectors: tx('存在待映射扇区', 'Pending sectors present'),
    ata_uncorrectable_sectors: tx('存在不可纠正扇区', 'Uncorrectable sectors present'),
    ata_historical_reallocated: tx('历史累计重映射', 'Historical reallocation'),
    ata_new_reallocated: tx('基线后新增重映射', 'New reallocation since baseline'),
    ata_historical_crc_errors: tx('历史累计链路 CRC 错误', 'Historical link CRC errors'),
    ata_new_crc_errors: tx('基线后新增链路 CRC 错误', 'New link CRC errors since baseline'),
    historical_ce_errors: tx('历史累计可纠正内存错误', 'Historical corrected memory errors'),
    historical_ue_errors: tx('历史累计不可纠正内存错误', 'Historical uncorrected memory errors'),
    new_ce_errors: tx('基线后新增可纠正错误', 'New corrected errors since baseline'),
    new_ue_errors: tx('基线后新增不可纠正错误', 'New uncorrected errors since baseline'),
    historical_media_errors: tx('历史累计介质错误', 'Historical cumulative media errors'),
    new_media_errors: tx('基线后新增介质错误', 'New media errors since baseline'),
    manufacturer_smart_failure: tx('厂商 SMART 故障标记', 'Manufacturer SMART failure'),
    nvme_endurance_consumed: tx('估计耐久度已消耗', 'Estimated endurance consumed'),
    nvme_spare_below_threshold: tx('备用容量低于阈值', 'Spare capacity below threshold'),
    nvme_temperature_threshold: tx('温度阈值告警', 'Temperature threshold warning'),
    nvme_reliability_degraded: tx('可靠性降级', 'Reliability degraded'),
    nvme_media_read_only: tx('介质只读', 'Media read-only'),
    nvme_volatile_backup_failed: tx('易失性备份失败', 'Volatile backup failed'),
    nvme_pmr_unreliable: tx('持久内存区域异常', 'Persistent memory region unreliable'),
  };
  return names[rule] || rule;
}
function State({ level, tx }: { level: HealthLevel; tx: Tx }) {
  const labels = { critical: tx('严重', 'Critical'), warning: tx('警告', 'Warning'), attention: tx('关注', 'Attention'), unknown: tx('未知', 'Unknown'), clear_for_checked_signals: tx('已检查项未命中', 'Checked signals clear') };
  const Icon = level === 'clear_for_checked_signals' ? CheckCircle2 : level === 'unknown' ? AlertTriangle : ShieldAlert;
  return <span className={`hh-state hh-${level}`}><Icon size={14} />{labels[level]}</span>;
}
function timestamp(value: string, lang: string): string {
  if (value.startsWith('0001')) return '—';
  return new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}
export default function HardwareHealth({ tx, lang }: { tx: Tx; lang: string }) {
  const [imported, setImported] = useState<ImportedHealthReport | null>(null);
  const [original, setOriginal] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [tab, setTab] = useState('disk');
  const [query, setQuery] = useState('');
  const [level, setLevel] = useState('');
  const [quality, setQuality] = useState('');
  const [media, setMedia] = useState('');
  const [order, setOrder] = useState('risk');
  const [page, setPage] = useState(0);
  const [size, setSize] = useState(25);
  const [selected, setSelected] = useState<string | null>(null);
  const [detailTab, setDetailTab] = useState('evidence');
  const fileInput = useRef<HTMLInputElement>(null);
  const revision = useRef(0);
  useEffect(() => () => { revision.current += 1; }, []);
  const rows = useMemo(() => imported?.report.rows ?? [], [imported]);
  const diskRows = useMemo(() => rows.filter(row => row.entity_type !== 'memory_controller'), [rows]);
  const memoryRows = useMemo(() => rows.filter(row => row.entity_type === 'memory_controller'), [rows]);
  const typeRows = useMemo(() => tab === 'disk' ? diskRows : tab === 'memory' ? memoryRows : [], [tab, diskRows, memoryRows]);
  const filtered = useMemo(() => selectRows(typeRows.filter(row => tab !== 'disk' || !media || `${row.protocol}:${row.disk?.media_type}` === media), query, level, quality, order), [typeRows, query, level, quality, order, media, tab]);
  const actualPage = Math.min(page, Math.max(0, Math.ceil(filtered.length / size) - 1));
  const visible = filtered.slice(actualPage * size, (actualPage + 1) * size);
  const detail = rows.find(row => row.reference === selected);
  const riskCount = rows.filter(row => ['critical', 'warning', 'attention'].includes(row.level)).length;
  const staleCount = rows.filter(row => row.freshness === 'stale').length;

  async function importFile(file?: File) {
    if (!file) return;
    const ticket = ++revision.current; setBusy(true); setError('');
    try {
      if (file.size > MAX_BUNDLE_BYTES) throw new Error('bundle_too_large');
      const content = await file.text();
      const value = await parseHealthBundle(content, file.name);
      if (ticket !== revision.current) return;
      setImported(value); setOriginal(content); setSelected(null); setTab(value.report.rows.some(row => row.entity_type !== 'memory_controller') ? 'disk' : 'memory'); setQuery(''); setLevel(''); setQuality(''); setMedia(''); setPage(0);
    } catch (cause) { if (ticket === revision.current) setError(cause instanceof Error ? cause.message : 'import_failed'); }
    finally { if (ticket === revision.current) setBusy(false); }
  }
  function clear() { revision.current += 1; setImported(null); setOriginal(''); setSelected(null); setTab('disk'); setQuery(''); setLevel(''); setQuality(''); setMedia(''); setPage(0); setError(''); setBusy(false); }
  function exportBundle() {
    if (!imported) return;
    const url = URL.createObjectURL(new Blob([original], { type: 'application/json' }));
    const link = document.createElement('a'); link.href = url; link.download = 'hardware-health-bundle.json'; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  return <div className="hh-page" aria-busy={busy}>
    <div className="hh-reportbar">
      <div><FileCheck2 size={18} /><div><b>{imported?.fileName || tx('未载入健康证据', 'No health evidence loaded')}</b><span>{tx('离线快照 · 非实时 · 生产采集未启用', 'Offline snapshot · not live · collection disabled')}</span></div></div>
      <div className="hh-actions">
        <input ref={fileInput} type="file" accept=".json,application/json" className="hh-file" aria-label={tx('选择报告文件', 'Select report file')} onChange={event => { void importFile(event.target.files?.[0]); event.target.value = ''; }} />
        <button className="hh-command" onClick={() => fileInput.current?.click()} disabled={busy}>{busy ? <Loader2 size={15} className="spin" /> : <Upload size={15} />}{tx('导入报告', 'Import report')}</button>
        <button className="icon-btn" onClick={exportBundle} disabled={!imported || busy} title={tx('导出原始报告', 'Export original report')} aria-label={tx('导出原始报告', 'Export original report')}><Download size={16} /></button>
        <button className="icon-btn" onClick={clear} disabled={!imported && !error && !busy} title={tx('清除本地报告', 'Clear local report')} aria-label={tx('清除本地报告', 'Clear local report')}><Trash2 size={16} /></button>
      </div>
    </div>
    {error && <div className="hh-error" role="alert"><AlertTriangle size={17} /><div><b>{tx('报告未通过校验', 'Report validation failed')}</b><code>{error}</code></div><button className="icon-btn" onClick={() => setError('')} aria-label={tx('关闭错误', 'Dismiss error')}><X size={15} /></button></div>}
    {imported && <div className="hh-provenance"><span>{tx('评估时点', 'Evaluation cutoff')} <b>{timestamp(imported.report.as_of, lang)}</b></span><span>{imported.report.rule_version}</span><span title={imported.sha256}>SHA256 <code>{imported.sha256.slice(0, 16)}</code></span><span className="hh-integrity"><CheckCircle2 size={13} />{tx('文件完整性已校验', 'File integrity checked')}</span></div>}
    <section className="hh-metrics" aria-label={tx('报告摘要', 'Report summary')}>
      {[{ label: tx('证据记录', 'Evidence records'), value: imported ? rows.length : '—', icon: HardDrive }, { label: tx('需复核风险', 'Risks to review'), value: imported ? riskCount : '—', icon: ShieldAlert }, { label: tx('数据不足', 'Insufficient data'), value: imported?.report.summary.unknown ?? '—', icon: AlertTriangle }, { label: tx('时点已过期', 'Stale at cutoff'), value: imported ? staleCount : '—', icon: Network }].map(metric => <div key={metric.label}><metric.icon size={17} /><span>{metric.label}</span><strong>{metric.value}</strong></div>)}
    </section>
    <div className="hh-tabs" role="tablist" aria-label={tx('硬件类型', 'Hardware type')}>
      {[['disk', tx('硬盘', 'Disks'), HardDrive], ['memory', tx('内存', 'Memory'), MemoryStick]].map(([id, label, Icon]) => { const I = Icon as typeof HardDrive; return <button key={String(id)} id={`hh-tab-${id}`} role="tab" aria-controls="hh-records" tabIndex={tab === id ? 0 : -1} onKeyDown={navigateTabs} aria-selected={tab === id} onClick={() => { setTab(String(id)); setSelected(null); setQuery(''); setLevel(''); setQuality(''); setMedia(''); setPage(0); }}><I size={15} />{String(label)}<span>{id === 'disk' ? diskRows.length : memoryRows.length}</span></button>; })}
    </div>
    <section className="hh-list" id="hh-records" role="tabpanel" aria-labelledby={`hh-tab-${tab}`}>
      <div className="hh-toolbar">
        <label className="hh-search"><Search size={15} /><input aria-label={tx('搜索健康记录', 'Search health records')} placeholder={tx('节点、序列号、型号或设备', 'Node, serial, model or device')} value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} /></label>
        <select aria-label={tx('风险等级', 'Risk level')} value={level} onChange={event => { setLevel(event.target.value); setPage(0); }}><option value="">{tx('全部等级', 'All levels')}</option>{levels.map(value => <option key={value} value={value}>{({ critical: tx('严重', 'Critical'), warning: tx('警告', 'Warning'), attention: tx('关注', 'Attention'), unknown: tx('未知', 'Unknown'), clear_for_checked_signals: tx('已检查项未命中', 'Checked signals clear') })[value]}</option>)}</select>
        <select aria-label={tx('数据状态', 'Data status')} value={quality} onChange={event => { setQuality(event.target.value); setPage(0); }}><option value="">{tx('全部数据状态', 'All data states')}</option><option value="assessed">{tx('已评估', 'Assessed')}</option><option value="insufficient_data">{tx('数据不足', 'Insufficient')}</option><option value="stale">{tx('时点已过期', 'Stale at cutoff')}</option></select>
        {tab === 'disk' && <select aria-label={tx('硬盘类型', 'Disk type')} value={media} onChange={event => { setMedia(event.target.value); setPage(0); }}><option value="">{tx('全部硬盘类型', 'All disk types')}</option><option value="ATA:HDD">SATA HDD</option><option value="ATA:SSD">SATA SSD</option><option value="NVMe:SSD">NVMe SSD</option></select>}
        <label className="hh-sort"><ArrowDownWideNarrow size={15} /><select aria-label={tx('排序', 'Sort order')} value={order} onChange={event => { setOrder(event.target.value); setPage(0); }}><option value="risk">{tx('风险优先', 'Risk first')}</option><option value="observed">{tx('观测时间', 'Observed time')}</option></select></label>
      </div>
      {tab === 'memory' && !memoryRows.length ? <div className="hh-empty"><AlertTriangle size={28} /><h2>{tx('当前报告无内存原始证据', 'No raw memory evidence in this report')}</h2><span>{tx('状态未知 · 未评分', 'Unknown · not scored')}</span></div> : !imported || !typeRows.length ? <div className="hh-empty"><HardDrive size={32} /><h2>{tx('尚无硬盘健康证据', 'No disk health evidence')}</h2><span>{tx('状态未知 · 未评分', 'Unknown · not scored')}</span></div> : <>
        <div className="hh-table-scroll"><table className="hh-table"><thead><tr><th>{tx('硬件 / 身份', 'Hardware / identity')}</th><th>{tx('节点 / 设备', 'Node / device')}</th><th>{tx('健康检测', 'Health detection')}</th><th>{tx('数据 / 适用性', 'Data / support')}</th><th>{tx('观测时间', 'Observed')}</th><th><span className="hh-sr-only">{tx('证据详情', 'Evidence detail')}</span></th></tr></thead><tbody>{visible.map(row => <tr key={row.reference} className={selected === row.reference ? 'hh-selected' : ''}><td><b>{row.model || row.reference}</b><code>{row.memory ? `${row.memory.controller || '—'} · ${tx('启动内身份', 'Boot-scoped identity')}` : row.serial || tx('身份缺失', 'Identity missing')}</code><small>{row.protocol || '—'} · {row.memory ? tx('控制器级 · DIMM 未定位', 'Controller scope · DIMM not located') : row.entity_type === 'nvme_controller' ? tx('控制器级', 'Controller scope') : tx('盘级', 'Disk scope')}</small></td><td><b>{row.node || '—'}</b><code>{row.device || '—'}</code><small>{row.source_kind === 'fixture' ? tx('测试样例', 'Fixture') : row.source_kind === 'operator_snapshot' ? tx('人工快照', 'Operator snapshot') : tx('来源未知', 'Source unknown')}</small></td><td><State level={row.level} tx={tx} /><small>{row.hits.length ? tx(`${row.hits.length} 条规则命中`, `${row.hits.length} rule hits`) : '—'}</small></td><td><span className={`hh-quality ${row.assessment_status === 'insufficient_data' ? 'hh-blocked' : ''}`}>{row.assessment_status === 'assessed' ? tx('时点可评估', 'Assessable at cutoff') : tx('数据不足', 'Insufficient data')}</span><small>{row.support === 'unsupported' ? tx('协议或型号未匹配', 'Protocol or model unmatched') : tx('有限规则 · 未经实机验证', 'Limited rules · not pilot verified')}</small><small>{row.blockers.length ? tx(`${row.blockers.length} 项阻断`, `${row.blockers.length} blockers`) : '—'}</small></td><td>{timestamp(row.observed_at, lang)}<small>{row.freshness === 'stale' ? tx('已过期', 'Stale') : row.freshness === 'fresh_at_cutoff' ? tx('报告时点有效', 'Fresh at report cutoff') : tx('时点未通过', 'Time not validated')}</small></td><td><button className="icon-btn" title={tx('查看证据', 'View evidence')} aria-label={`${tx('查看证据', 'View evidence')} ${row.reference}`} onClick={() => { setSelected(row.reference); setDetailTab('evidence'); }}><ChevronRight size={16} /></button></td></tr>)}</tbody></table>{visible.length === 0 && <div className="hh-empty hh-empty-filter"><Search size={25} /><h2>{tx('没有匹配记录', 'No matching records')}</h2><button className="hh-command" onClick={() => { setQuery(''); setLevel(''); setQuality(''); setMedia(''); setPage(0); }}>{tx('重置筛选', 'Reset filters')}</button></div>}</div>
        <div className="hh-pagination"><span>{filtered.length ? actualPage * size + 1 : 0}–{Math.min((actualPage + 1) * size, filtered.length)} / {filtered.length}</span><div><select aria-label={tx('每页条数', 'Page size')} value={size} onChange={event => { setSize(Number(event.target.value)); setPage(0); }}>{[25, 50, 100].map(value => <option key={value} value={value}>{value} / {tx('页', 'page')}</option>)}</select><button className="icon-btn" disabled={actualPage === 0} onClick={() => setPage(actualPage - 1)} title={tx('上一页', 'Previous page')} aria-label={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button className="icon-btn" disabled={(actualPage + 1) * size >= filtered.length} onClick={() => setPage(actualPage + 1)} title={tx('下一页', 'Next page')} aria-label={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div>
      </>}
    </section>
    {detail && <EvidenceDetail row={detail} tx={tx} lang={lang} activeTab={detailTab} setTab={setDetailTab} close={() => setSelected(null)} />}
  </div>;
}

function EvidenceDetail({ row, tx, lang, activeTab, setTab, close }: { row: HealthRow; tx: Tx; lang: string; activeTab: string; setTab: (value: string) => void; close: () => void }) {
  const hitByEvidence = (id: string) => row.hits.filter(hit => hit.evidence_id === id);
  return <section className="hh-detail" aria-label={tx('证据详情', 'Evidence detail')}>
    <header><div><h2>{row.model || row.reference}</h2><code>{row.reference}</code></div><State tx={tx} level={row.level} /><button className="icon-btn" onClick={close} aria-label={tx('关闭证据', 'Close evidence')} title={tx('关闭证据', 'Close evidence')}><X size={16} /></button></header>
    {row.disk && <DiskHealthOverview row={row} tx={tx} />}
    <div className="hh-detail-tabs" role="tablist" aria-label={tx('证据分区', 'Evidence sections')}>{[['evidence', tx('规则与字段', 'Rules & fields')], ['blockers', tx('数据缺口', 'Data gaps')], ['source', tx('来源与身份', 'Source & identity')]].map(([id, label]) => <button key={id} role="tab" tabIndex={activeTab === id ? 0 : -1} onKeyDown={navigateTabs} aria-selected={activeTab === id} onClick={() => setTab(id)}>{label}{id === 'blockers' && <span>{row.blockers.length}</span>}</button>)}</div>
    {activeTab === 'evidence' && <div className="hh-table-scroll"><table className="hh-evidence-table"><thead><tr><th>{tx('字段 / 证据 ID', 'Field / evidence ID')}</th><th>{tx('精确值', 'Exact value')}</th><th>{tx('单位', 'Unit')}</th><th>{tx('命中规则', 'Matched rules')}</th></tr></thead><tbody>{row.evidence.map(item => <tr key={item.id}><td><code>{item.field}</code><small title={item.id}>{item.id.slice(0, 16)}</small></td><td><code>{item.value}</code></td><td>{item.unit}</td><td>{hitByEvidence(item.id).map(hit => <div className="hh-hit" key={hit.rule}><State tx={tx} level={hit.level} /><span>{ruleTitle(hit.rule, tx)}</span><code>{hit.rule}</code></div>)}</td></tr>)}</tbody></table>{!row.evidence.length && <div className="hh-empty hh-empty-filter">{tx('无可解析字段', 'No parsable fields')}</div>}</div>}
    {activeTab === 'blockers' && <div className="hh-gaps">{row.blockers.length ? row.blockers.map(value => <div key={value}><AlertTriangle size={15} /><code>{value}</code></div>) : <span>{tx('本次有限规则无数据阻断', 'No blockers for the checked subset')}</span>}<dl><dt>{tx('趋势状态', 'Trend status')}</dt><dd>{row.trend_status}</dd>{row.memory ? <><dt>{tx('CE 新增量', 'CE delta')}</dt><dd>{row.memory.ce_delta ?? '—'}</dd><dt>{tx('UE 新增量', 'UE delta')}</dt><dd>{row.memory.ue_delta ?? '—'}</dd><dt>{tx('定位精度', 'Location precision')}</dt><dd>{tx('控制器级 · DIMM 未定位', 'Controller scope · DIMM not located')}</dd></> : <><dt>{tx('介质错误新增量', 'Media error delta')}</dt><dd>{row.media_delta ?? '—'}</dd></>}</dl></div>}
    {activeTab === 'source' && <dl className="hh-source">{[[tx('实体标识', 'Entity ID'), row.entity_id], [tx('身份状态', 'Identity status'), row.identity_status], [tx('范围 / 代次', 'Scope / generation'), `${row.scope || '—'} / ${row.generation || '—'}`], [tx('启动标识', 'Boot ID'), row.boot_id || '—'], [row.memory ? tx('驱动 / 内核', 'Driver / kernel') : tx('序列号 / 固件', 'Serial / firmware'), row.memory ? `${row.memory.driver || '—'} / ${row.memory.kernel_release || '—'}` : `${row.serial || '—'} / ${row.firmware || '—'}`], [tx('工具 / 来源', 'Tool / source'), `${row.tool_version || '—'} / ${row.source || '—'}`], [tx('原始输入 SHA256', 'Raw input SHA256'), row.input_sha256], [tx('基线 SHA256', 'Baseline SHA256'), row.baseline_sha256 || '—'], [tx('观测 / 可用时间', 'Observed / available'), `${timestamp(row.observed_at, lang)} / ${timestamp(row.available_at, lang)}`], [tx('退出码', 'Exit code'), row.exit_code === null ? '—' : String(row.exit_code)]].map(([label, value]) => <div key={label}><dt>{label}</dt><dd><code>{value}</code></dd></div>)}</dl>}
  </section>;
}

function DiskHealthOverview({ row, tx }: { row: HealthRow; tx: Tx }) {
  const disk = row.disk;
  if (!disk) return null;
  const names = { media: tx('介质状态', 'Media'), endurance: tx('耐久状态', 'Endurance'), temperature: tx('温度状态', 'Temperature'), path_power: tx('链路 / 供电', 'Link / power') };
  return <div className="hh-disk-overview" aria-label={tx('硬盘健康维度', 'Disk health dimensions')}>
    <dl className="hh-disk-readings">
      <div><dt>{tx('物理介质', 'Physical media')}</dt><dd>{row.protocol} / {disk.media_type}</dd></div>
      <div><dt>{tx('当前温度', 'Current temperature')}</dt><dd>{disk.temperature_celsius === null ? '—' : `${disk.temperature_celsius} °C`}</dd></div>
      <div><dt>{disk.wear?.kind === 'consumed_percent' ? tx('耐久消耗估计', 'Estimated endurance consumed') : tx('厂商归一化磨损值', 'Vendor normalized wear')}</dt><dd>{disk.wear ? `${disk.wear.value}${disk.wear.kind === 'consumed_percent' ? '%' : ''}` : '—'}</dd></div>
      <div><dt>{tx('适用规则', 'Rule profile')}</dt><dd><code>{disk.profile || tx('未匹配', 'Unmatched')}</code></dd></div>
    </dl>
    <div className="hh-dimensions">{disk.dimensions.map(dimension => <div key={dimension.id}><b>{names[dimension.id]}</b><State level={dimension.level} tx={tx} /><small>{dimension.state === 'not_applicable' ? tx('不适用', 'Not applicable') : dimension.state === 'insufficient_data' ? tx('覆盖不足', 'Incomplete coverage') : tx('有限检查', 'Limited checks')}</small>{row.hits.filter(hit => diskDimensionForRule(hit.rule) === dimension.id).map(hit => <span key={hit.rule}>{ruleTitle(hit.rule, tx)}</span>)}</div>)}</div>
    <dl className="hh-disk-deltas"><div><dt>{tx('趋势状态', 'Trend state')}</dt><dd>{row.trend_status}</dd></div>{disk.counter_deltas.map(delta => <div key={delta.field}><dt><code>{delta.field}</code></dt><dd>+{delta.value} {delta.unit}</dd></div>)}{row.media_delta !== null && <div><dt>{tx('介质错误新增', 'New media errors')}</dt><dd>+{row.media_delta}</dd></div>}<div><dt>{tx('故障概率 / 剩余寿命', 'Failure probability / remaining life')}</dt><dd>{tx('未建立模型', 'Model not established')}</dd></div></dl>
  </div>;
}
