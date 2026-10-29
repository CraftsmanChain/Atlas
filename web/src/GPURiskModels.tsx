import { useEffect, useState } from 'react';

type Tx = (zh: string, en: string) => string;
type Model = { id: number; status: string; artifact_sha: string; preprocessor_sha: string; training_through: string; available_at: string };
type Batch = { id: number; model_id: number; status: string; error_code: string; request_sha: string; result_sha: string; artifact_sha: string };
type Scores = { data: { node_id: number; generation: number; raw_score: number | null; signal_status: string; rank: number; tie_count: number; eligible_count: number; top_five_percent_k: number; blockers: string[]; warnings: string[] }[]; members: { node_id: number; node_ip: string }[]; batch: Batch; meta: { total_nodes: number; has_more: boolean; next_after_node_id: number } };

export default function GPURiskModels({ tx, queryID, refreshKey }: { tx: Tx; queryID: number; refreshKey: number }) {
  const [models, setModels] = useState<Model[] | null>(null);
  const [batches, setBatches] = useState<Batch[]>([]);
  const [selection, setSelection] = useState({ queryID: 0, batchID: 0 });
  const [after, setAfter] = useState(0);
  const [previous, setPrevious] = useState<number[]>([]);
  const [scores, setScores] = useState<Scores | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const batchID = selection.queryID === queryID ? selection.batchID : 0;
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const response = await fetch('/api/v1/gpu-risk/models?limit=50', { signal: controller.signal });
        if (!response.ok) throw new Error(`Models HTTP ${response.status}`);
        const payload = await response.json() as { data: Model[] };
        setModels(payload.data);
        if (queryID) {
          const response = await fetch(`/api/v1/gpu-risk/queries/${queryID}/model-batches?limit=50`, { signal: controller.signal });
          if (!response.ok) throw new Error(`Batches HTTP ${response.status}`);
          const payload = await response.json() as { data: Batch[] };
          setBatches(payload.data);
        } else setBatches([]);
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : 'Read failed'); }
    })();
    return () => controller.abort();
  }, [queryID, refreshKey]);
  useEffect(() => {
    const controller = new AbortController();
    setScores(null); setError(''); setBusy(false);
    if (!batchID || !queryID) return () => controller.abort();
    setBusy(true);
    void (async () => {
      try {
        const response = await fetch(`/api/v1/gpu-risk/queries/${queryID}/model-scores?batch_id=${batchID}&limit=100${after ? `&after_node_id=${after}` : ''}`, { signal: controller.signal });
        if (!response.ok) throw new Error(`Scores HTTP ${response.status}`);
        setScores(await response.json() as Scores);
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : 'Read failed'); }
      finally { if (!controller.signal.aborted) setBusy(false); }
    })();
    return () => controller.abort();
  }, [queryID, batchID, after, refreshKey]);
  const state = (value: string) => ({ offline_candidate: tx('未晋级候选', 'Unpromoted candidate'), completed: tx('评分完成', 'Completed'), blocked: tx('范围阻断', 'Blocked'), failed: tx('评分失败', 'Failed'), scored: tx('已评分', 'Scored'), scored_with_unknowns: tx('含未知特征评分', 'Scored with unknown features') }[value] || value);
  return <section className="gpu-risk-candidates" aria-busy={busy}>
    <h3>{tx('MLP 候选模型与冻结评分', 'MLP candidates and frozen scores')}</h3>
    <p>{tx('仅展示未晋级候选；分数不是故障概率。候选与原基线独立，未启用自动评分或通知。未知特征显式编码，训练实体重叠、身份不足和来源冲突不参与排名。', 'Unpromoted candidates only; scores are not failure probabilities. Candidate batches are separate from baselines, with no automatic scoring or notifications. Unknown features are encoded explicitly; training overlap, incomplete identity and source conflicts remain unranked.')}</p>
    {error && <p role="alert">{error}</p>}
    {models === null ? <p>{tx('候选列表尚未读取完成。', 'Candidate list has not finished loading.')}</p> : !models.length ? <p>{tx('尚无通过来源与数值校验的候选模型。', 'No candidate has passed provenance and numeric verification yet.')}</p> : <details><summary>{tx('最近50个候选模型', 'Most recent 50 candidates')}</summary>{models.map(model => <p key={model.id}>#{model.id} · {state(model.status)} · {tx('训练截止', 'Training through')}: {model.training_through} · {tx('可用时间', 'Available')}: {model.available_at}<small>Artifact SHA: {model.artifact_sha} · Preprocessor SHA: {model.preprocessor_sha}</small></p>)}</details>}
    {queryID > 0 && <label>{tx('选择固定评分批次', 'Select a fixed score batch')} <select value={batchID} onChange={event => { setSelection({ queryID, batchID: Number(event.target.value) }); setAfter(0); setPrevious([]); }}><option value={0}>{tx('请选择，不自动使用最新批次', 'Select explicitly; no latest-batch default')}</option>{batches.map(batch => <option key={batch.id} value={batch.id}>#{batch.id} · Model #{batch.model_id} · {state(batch.status)}{batch.error_code ? ` · ${batch.error_code}` : ''}</option>)}</select></label>}
    {queryID > 0 && !batches.length && <p>{tx('本 query 尚无候选评分批次。', 'No candidate scores recorded for this query.')}</p>}
    {scores && <><p>query #{queryID} · batch #{scores.batch.id} · model #{scores.batch.model_id} · {state(scores.batch.status)} · {tx('完整范围', 'Full scope')}: {scores.meta.total_nodes} · {tx('可排名', 'Rankable')}: {scores.data[0]?.eligible_count ?? 0} · Top-5% K: {scores.data[0]?.top_five_percent_k ?? 0}</p>
      {scores.batch.error_code && <p role="alert">{scores.batch.error_code}</p>}
      <div className="gpu-risk-table"><table><thead><tr>{[tx('节点', 'Node'), tx('代际', 'Generation'), tx('名次 / 并列数', 'Rank / tie size'), tx('Logit分数', 'Logit score'), tx('状态 / 阻断', 'Status / blockers')].map(label => <th key={label}>{label}</th>)}</tr></thead><tbody>{scores.data.map(row => <tr key={row.node_id}><td>{scores.members.find(member => member.node_id === row.node_id)?.node_ip || row.node_id}</td><td>{row.generation}</td><td>{row.rank ? `${row.rank} / ${row.tie_count}` : '—'}</td><td>{row.raw_score ?? '—'}</td><td>{state(row.signal_status)}<small>{row.blockers.join(' · ')}</small><small>{row.warnings.join(' · ')}</small></td></tr>)}</tbody></table></div>
      <div className="gpu-risk-toolbar"><button disabled={!previous.length || busy} onClick={() => { setAfter(previous[previous.length - 1]); setPrevious(previous.slice(0, -1)); }}>{tx('上一页', 'Previous')}</button><button disabled={!scores.meta.has_more || busy} onClick={() => { setPrevious([...previous, after]); setAfter(scores.meta.next_after_node_id); }}>{tx('下一页', 'Next')}</button></div>
      <details><summary>{tx('模型与请求指纹', 'Model and request fingerprints')}</summary><p>Artifact SHA: {scores.batch.artifact_sha}</p><p>Request SHA: {scores.batch.request_sha}</p><p>Result SHA: {scores.batch.result_sha}</p></details>
    </>}
  </section>;
}
