import { useEffect, useRef, useState } from 'react';
import './GPURisk.css';
import GPURiskModels from './GPURiskModels';
import GPURiskOperations from './GPURiskOperations';

type Tx = (zh: string, en: string) => string;
type Query = { id: number; cutoff: string; member_count: number; scope_sha: string; result_sha: string; target: string; horizon_minutes: number };
type Overview = { status: string; latest: Query | null; jobs: { id: number; status: string; error_code: string }[]; automatic_jobs_enabled: boolean; outcome_jobs_enabled: boolean; blockers: string[] };
type Score = { node_id: number; raw_score: number | null; signal_status: string; rank: number; tie_count: number; eligible_count: number; top_five_percent_k: number; coverage: number; evidence: string[] };
type Member = { node_id: number; node_ip: string; generation: number; status: string; reason: string };
type Result = { data: Score[]; members: Member[]; query: Query; meta: { total_nodes: number; has_more: boolean; next_after_node_id: number } };

type Outcome = { node_id: number; generation: number; status: string; positive: boolean | null; intervention: boolean; episode_keys: string[]; blockers: string[] };
type Outcomes = { data: Outcome[]; batch: { id: number; as_of: string; sha: string } | null };

export default function GPURisk({ tx }: { tx: Tx }) {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const [outcomes, setOutcomes] = useState<Outcomes | null>(null);
  const pinnedBatch = useRef<number>(0);
  const [error, setError] = useState('');
  const [policy, setPolicy] = useState('health-inverse-v1');
  const [after, setAfter] = useState(0);
  const [cursors, setCursors] = useState<number[]>([]);
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const pinnedQuery = useRef<Query | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true); setError(''); setResult(null); setOutcomes(null);
    void (async () => {
      try {
        const response = await fetch('/api/v1/gpu-risk/overview', { signal: controller.signal });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json() as { data: Overview };
        const query = after ? pinnedQuery.current : payload.data.latest;
        if (!after) pinnedQuery.current = query;
        setOverview({ ...payload.data, latest: query, status: query && Date.now() - Date.parse(query.cutoff) > 25 * 3600 * 1000 ? 'stale' : payload.data.status });
        if (query) {
          const nodes = await fetch(`/api/v1/gpu-risk/queries/${query.id}/nodes?policy=${policy}&limit=100${after ? `&after_node_id=${after}` : ''}`, { signal: controller.signal });
          if (!nodes.ok) throw new Error(`HTTP ${nodes.status}`);
          setResult(await nodes.json() as Result);
          if (!after || pinnedBatch.current) {
            const response = await fetch(`/api/v1/gpu-risk/queries/${query.id}/outcomes?limit=100${after ? `&after_node_id=${after}&batch_id=${pinnedBatch.current}` : ''}`, { signal: controller.signal });
            if (!response.ok) throw new Error(`Outcome HTTP ${response.status}`);
            const data = await response.json() as Outcomes;
            if (!after) pinnedBatch.current = data.batch?.id || 0;
            setOutcomes(data);
          }
        }
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : 'Request failed'); }
      finally { if (!controller.signal.aborted) setBusy(false); }
    })();
    return () => controller.abort();
  }, [policy, after, refresh]);
  const state = (value: string) => ({ collecting: tx('积累中', 'Collecting'), ready: tx('快照可用', 'Snapshot ready'), stale: tx('快照已过期', 'Stale snapshot'), complete: tx('信号完整', 'Complete signal'), partial: tx('部分信号，不参与排名', 'Partial signal, unranked'), no_signal: tx('无信号', 'No signal'), blocked: tx('身份待确认', 'Identity blocked'), excluded: tx('范围排除', 'Excluded'), included: tx('固定等分对照', 'Uniform control'), pending_horizon: tx('标签待成熟', 'Label maturing'), pending_watermark: tx('等待完整来源', 'Waiting for source coverage'), matured_positive: tx('已确认新故障', 'Confirmed new failure'), matured_negative: tx('完整观察未见新故障', 'No new failure with complete observation'), censored: tx('观察不完整', 'Censored'), ambiguous: tx('起点模糊', 'Ambiguous onset'), intervention_affected: tx('受干预影响', 'Intervention affected'), identity_conflict: tx('身份冲突', 'Identity conflict') }[value] || value);
  const reset = () => { setAfter(0); setCursors([]); };
  return <section className="gpu-risk" aria-busy={busy}>
    <div className="gpu-risk-toolbar"><h2>{tx('节点风险排名', 'Node risk ranking')}</h2><span className="gpu-risk-tag">{tx('影子运行 · 效果验证中', 'Shadow · under validation')}</span><button onClick={() => { reset(); setRefresh(x => x + 1); }}>{tx('刷新', 'Refresh')}</button></div>
    {error && <p role="alert">{tx('读取失败：', 'Read failed: ')}{error}</p>}
    {!overview && busy && <p role="status">{tx('加载中…', 'Loading…')}</p>}
    {overview && <><p role="status"><b>{state(overview.status)}</b> · {overview.automatic_jobs_enabled ? tx('日批已启用', 'Daily jobs enabled') : tx('日批已关闭', 'Daily jobs disabled')} · {overview.outcome_jobs_enabled ? tx('结果任务已启用', 'Outcome jobs enabled') : tx('结果任务已关闭', 'Outcome jobs disabled')} · {tx('完整范围', 'Full scope')}: {overview.latest?.member_count ?? 0} {tx('节点', 'nodes')} · {tx('数据时间', 'As of')}: {overview.latest?.cutoff || '—'}</p>
      {overview.jobs.some(job => job.status === 'failed') && <p role="alert">{tx('有失败任务，显示上次完整榜单', 'Job failed; showing last complete ranking')}</p>}
      {!overview.latest && <p>{tx('尚无完整批次', 'No complete query yet')}</p>}
    </>}
    <label>{tx('对照策略', 'Policy')} <select value={policy} onChange={event => { setPolicy(event.target.value); reset(); }}><option value="health-inverse-v1">{tx('当前健康反分', 'Inverse current health')}</option><option value="uniform-v1">{tx('固定等分对照', 'Uniform control')}</option></select></label>
    {result && <><p>{tx('可排名节点', 'Eligible nodes')}: {result.data[0]?.eligible_count ?? 0} · Top-5% K: {result.data[0]?.top_five_percent_k ?? 0}</p>
      <div className="gpu-risk-table"><table><thead><tr>{[tx('节点', 'Node'), tx('代际', 'Generation'), tx('名次 / 并列数', 'Rank / tie size'), tx('分数', 'Score'), tx('覆盖', 'Coverage'), tx('状态 / 原因', 'Status / reason'), tx('未来结果', 'Future outcome')].map(label => <th key={label}>{label}</th>)}</tr></thead><tbody>{result.data.map(score => {
        const member = result.members.find(row => row.node_id === score.node_id);
        const outcome = outcomes?.data.find(row => row.node_id === score.node_id && row.generation === member?.generation);
        return <tr key={score.node_id}><td>{member?.node_ip || score.node_id}</td><td>{member?.generation}</td><td>{score.rank ? `${score.rank} / ${score.tie_count}` : '—'}</td><td>{score.raw_score ?? '—'}</td><td>{(score.coverage * 100).toFixed(1)}%</td><td>{state(score.signal_status)}<small>{member?.reason}</small><details><summary>{tx('证据', 'Evidence')}</summary>{score.evidence.join(' · ') || tx('尚无健康信号', 'No health signal')}</details></td><td>{outcome ? state(outcome.status) : tx('尚未记录结果', 'No recorded outcome')}<small>{outcome?.blockers.join(' · ')}</small>{outcome?.intervention && <small>{tx('含维修/干预', 'Repair/intervention recorded')}</small>}</td></tr>;
      })}</tbody></table></div>
      <div className="gpu-risk-toolbar"><button disabled={!cursors.length || busy} onClick={() => { setAfter(cursors[cursors.length - 1]); setCursors(cursors.slice(0, -1)); }}>{tx('上一页', 'Previous')}</button><button disabled={!result.meta.has_more || busy} onClick={() => { setCursors([...cursors, after]); setAfter(result.meta.next_after_node_id); }}>{tx('下一页', 'Next')}</button></div>
      {outcomes?.batch && <p>{tx('结果批次 / as_of', 'Outcome batch / as_of')}: #{outcomes.batch.id} · {outcomes.batch.as_of}</p>}
      <details><summary>{tx('批次与验证边界', 'Query and validation boundaries')}</summary><p>query #{result.query.id} · {result.query.target} · {result.query.horizon_minutes} min</p><p>Scope SHA: {result.query.scope_sha}</p><p>Result SHA: {result.query.result_sha}</p></details>
    </>}
    <details className="gpu-risk-more"><summary>{tx('运行状态', 'Operations')}</summary><GPURiskOperations tx={tx} refreshKey={refresh} queryID={result?.query.id} /></details>
    <details className="gpu-risk-more"><summary>{tx('候选模型', 'Candidate models')}</summary><GPURiskModels tx={tx} queryID={overview?.latest?.id || 0} refreshKey={refresh} /></details>
  </section>;
}
