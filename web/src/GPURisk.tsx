import { useEffect, useRef, useState } from 'react';
import './GPURisk.css';
import GPURiskModels from './GPURiskModels';

type Tx = (zh: string, en: string) => string;
type Query = { id: number; cutoff: string; member_count: number; scope_sha: string; result_sha: string; target: string; horizon_minutes: number };
type Overview = { status: string; latest: Query | null; jobs: { id: number; status: string; error_code: string }[]; automatic_jobs_enabled: boolean; outcome_jobs_enabled: boolean; blockers: string[] };
type Score = { node_id: number; raw_score: number | null; signal_status: string; rank: number; tie_count: number; eligible_count: number; top_five_percent_k: number; coverage: number; evidence: string[] };
type Member = { node_id: number; node_ip: string; generation: number; status: string; reason: string };
type Result = { data: Score[]; members: Member[]; query: Query; meta: { total_nodes: number; has_more: boolean; next_after_node_id: number } };

type Outcome = { node_id: number; generation: number; status: string; positive: boolean | null; intervention: boolean; episode_keys: string[]; blockers: string[] };
type Outcomes = { data: Outcome[]; batch: { id: number; as_of: string; sha: string } | null };

export default function GPURisk({ tx, feedback }: { tx: Tx; feedback: () => void }) {
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
  const state = (value: string) => ({ collecting: tx('积累中', 'Collecting'), ready: tx('快照可用', 'Snapshot ready'), stale: tx('快照已过期', 'Stale snapshot'), complete: tx('信号完整', 'Complete signal'), partial: tx('部分信号，不参与排名', 'Partial signal, unranked'), no_signal: tx('无信号', 'No signal'), blocked: tx('身份待确认', 'Identity blocked'), excluded: tx('范围排除', 'Excluded'), included: tx('固定等分对照', 'Uniform control'), pending_horizon: tx('等待7天窗口', 'Waiting for horizon'), pending_watermark: tx('等待完整来源', 'Waiting for source coverage'), matured_positive: tx('已确认新故障', 'Confirmed new failure'), matured_negative: tx('完整观察未见新故障', 'No new failure with complete observation'), censored: tx('观察不完整', 'Censored'), ambiguous: tx('起点模糊', 'Ambiguous onset'), intervention_affected: tx('受干预影响', 'Intervention affected'), identity_conflict: tx('身份冲突', 'Identity conflict') }[value] || value);
  const reset = () => { setAfter(0); setCursors([]); };
  return <section className="gpu-risk" aria-busy={busy}>
    <div className="gpu-risk-toolbar"><h2>{tx('未来7天 · 节点风险优先级', 'Next 7 days · Node risk priority')}</h2><button onClick={() => { reset(); setRefresh(x => x + 1); }}>{tx('刷新', 'Refresh')}</button><button onClick={feedback}>{tx('故障登记与复核', 'Fault feedback and review')}</button></div>
    <p>{tx('当前为只读基线榜单，排序价值待验证。分数是当前健康反分或等分对照；MLP候选需单独选择固定批次查看；未来结果依赖已复核故障与来源完整性。', 'Read-only baseline with unverified ranking value. Scores use inverse current health or a uniform control; MLP candidates require an explicitly selected frozen batch. Future outcomes require reviewed incidents and complete source coverage.')}</p>
    {error && <p role="alert">{tx('读取失败，可重试：', 'Read failed, retry: ')}{error}</p>}
    {!overview && busy && <p role="status">{tx('加载中…', 'Loading…')}</p>}
    {overview && <><p role="status"><b>{state(overview.status)}</b> · {overview.automatic_jobs_enabled ? tx('日批已启用', 'Daily jobs enabled') : tx('日批已关闭', 'Daily jobs disabled')} · {overview.outcome_jobs_enabled ? tx('结果任务已启用', 'Outcome jobs enabled') : tx('结果任务已关闭', 'Outcome jobs disabled')} · {tx('完整范围', 'Full scope')}: {overview.latest?.member_count ?? 0} {tx('节点', 'nodes')} · {tx('数据时间', 'As of')}: {overview.latest?.cutoff || '—'}</p>
      {overview.jobs.some(job => job.status === 'failed') && <p role="alert">{tx('存在失败任务，保留上次完整榜单。', 'A job failed; the last complete snapshot is retained.')}</p>}
      {!overview.latest && <p>{tx('尚无完整批次。完成一次受控记录后，这里将显示所有节点；零故障样本也可以积累。', 'No complete query yet. A controlled recording will show every node; collection works with zero failure samples.')}</p>}
    </>}
    <label>{tx('对照策略', 'Policy')} <select value={policy} onChange={event => { setPolicy(event.target.value); reset(); }}><option value="health-inverse-v1">{tx('当前健康反分', 'Inverse current health')}</option><option value="uniform-v1">{tx('固定等分对照', 'Uniform control')}</option></select></label>
    {result && <><p>{tx('可排名节点', 'Eligible nodes')}: {result.data[0]?.eligible_count ?? 0} · Top-5% K: {result.data[0]?.top_five_percent_k ?? 0} · {tx('并列名次不表示节点间存在风险差异。列表按节点编号分页。', 'Ties do not imply risk differences. Rows are paginated by node ID.')}</p>
      <div className="gpu-risk-table"><table><thead><tr>{[tx('节点', 'Node'), tx('代际', 'Generation'), tx('名次 / 并列数', 'Rank / tie size'), tx('分数', 'Score'), tx('覆盖', 'Coverage'), tx('状态 / 原因', 'Status / reason'), tx('未来结果', 'Future outcome')].map(label => <th key={label}>{label}</th>)}</tr></thead><tbody>{result.data.map(score => {
        const member = result.members.find(row => row.node_id === score.node_id);
        const outcome = outcomes?.data.find(row => row.node_id === score.node_id && row.generation === member?.generation);
        return <tr key={score.node_id}><td>{member?.node_ip || score.node_id}</td><td>{member?.generation}</td><td>{score.rank ? `${score.rank} / ${score.tie_count}` : '—'}</td><td>{score.raw_score ?? '—'}</td><td>{(score.coverage * 100).toFixed(1)}%</td><td>{state(score.signal_status)}<small>{member?.reason}</small><details><summary>{tx('证据', 'Evidence')}</summary>{score.evidence.join(' · ') || tx('尚无健康信号', 'No health signal')}</details></td><td>{outcome ? state(outcome.status) : tx('尚未记录结果', 'No recorded outcome')}<small>{outcome?.blockers.join(' · ')}</small>{outcome?.intervention && <small>{tx('含维修/干预', 'Repair/intervention recorded')}</small>}</td></tr>;
      })}</tbody></table></div>
      <div className="gpu-risk-toolbar"><button disabled={!cursors.length || busy} onClick={() => { setAfter(cursors[cursors.length - 1]); setCursors(cursors.slice(0, -1)); }}>{tx('上一页', 'Previous')}</button><button disabled={!result.meta.has_more || busy} onClick={() => { setCursors([...cursors, after]); setAfter(result.meta.next_after_node_id); }}>{tx('下一页', 'Next')}</button></div>
      {outcomes?.batch && <p>{tx('结果批次 / as_of', 'Outcome batch / as_of')}: #{outcomes.batch.id} · {outcomes.batch.as_of}</p>}
      <details><summary>{tx('批次与验证边界', 'Query and validation boundaries')}</summary><p>query #{result.query.id} · {result.query.target} · {result.query.horizon_minutes} min</p><p>Scope SHA: {result.query.scope_sha}</p><p>Result SHA: {result.query.result_sha}</p><p>{tx('效果待验证；缺故障来源或完整性证明时保留未知，不将空台账解释为负例。', 'Value unverified. Missing incidents or completeness evidence remain unknown, not negatives.')}</p></details>
    </>}
    <GPURiskModels tx={tx} queryID={overview?.latest?.id || 0} refreshKey={refresh} />
  </section>;
}
