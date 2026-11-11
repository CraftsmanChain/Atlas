import { useEffect, useState } from 'react';
import { count, loadOperations, operationsView, text, type OperationsLoad } from './operationsView';
type Tx = (zh: string, en: string) => string;
export function OperationsContent({ data, tx, queryID }: { data: unknown; tx: Tx; queryID?: number }) {
  const view = operationsView(data);
  const unknown = tx('未评估', 'Not assessed');
  const number = (v: unknown) => count(v) ?? unknown;
  const flag = (v: boolean | null) => v === null ? unknown : v ? tx('已启用', 'Enabled') : tx('已关闭', 'Disabled');
  const status = (v: unknown) => ({ completed: tx('执行完成', 'Execution completed'), failed: tx('执行失败', 'Execution failed'), blocked: tx('执行受阻', 'Execution blocked'), running: tx('执行中', 'Running'), pending: tx('等待执行', 'Pending'), within_query_tail: tx('核查在允许时限内', 'Check within allowed lag'), invalid: tx('核查无效', 'Invalid check'), stale_for_query: tx('核查已过期', 'Check stale for query'), inspection_failed: tx('读取失败', 'Inspection failed'), no_completed_query: tx('尚无完整批次', 'No completed query') }[text(v)] || tx('状态未知', 'Unknown status'));
  const coverage = view.coverage;
  return <>
    <p>{tx('服务端日期', 'Server date')}: {view.day || unknown}</p>
    <div className="gpu-risk-operations-flags">{[tx('持久编排', 'Durable orchestration'), tx('日排名', 'Daily ranking'), tx('结果日批', 'Daily outcomes')].map((label, i) => <span key={label}>{label}: <b>{flag(view.flags[i])}</b></span>)}</div>
    <div className="gpu-risk-operations-stages">{view.stages.map(stage => <article key={stage.stage}><h4>{stage.stage === 'ranking' ? tx('今日日排名', 'Today’s ranking') : tx('今日结果日批', 'Today’s outcomes')}</h4>
      {!view.day || !view.recordsKnown ? <p>{unknown}</p> : !stage.records.length ? <p>{tx('今日尚无记录', 'No record for today')}</p> : stage.records.map((r, i) => <div key={i}><b>{status(r.status)}</b><p>Query #{number(r.query_id)} · {tx('节点', 'Nodes')}: {number(r.member_count)} · {tx('任务数', 'Jobs')}: {number(r.job_count)}</p><p>{tx('预算耗尽任务', 'Exhausted jobs')}: {number(r.exhausted)}</p><small>{tx('最近内容变化', 'Last content change')}: {text(r.observed_at) || unknown}</small>{text(r.error_code) && <details><summary>{tx('错误详情', 'Error details')}</summary><code>{text(r.error_code)}</code></details>}</div>)}</article>)}</div>
    <p>{tx('执行完成不代表标签成熟或模型效果通过；最近内容变化时间不是服务心跳。数据门由独立评估决定。', 'Completed execution does not establish label maturity or model value. Content-change time is not a heartbeat. Data gates require independent assessment.')}</p>
    <h4>{tx('最新批次覆盖记录', 'Latest query coverage records')}</h4>
    {count(coverage.query_id) === null || count(coverage.query_id) === 0 ? <p>{status(coverage.status)}</p> : <>
      <p>Query #{number(coverage.query_id)} · {tx('快照时间', 'Cutoff')}: {text(coverage.cutoff) || unknown} · {tx('核对时点', 'As of')}: {text(coverage.as_of) || unknown}</p>
      {!!queryID && queryID !== coverage.query_id && <p>{tx('此处是最新批次覆盖，当前固定榜单为另一批次', 'This coverage belongs to the latest query; the pinned ranking is a different query')}: #{queryID}</p>}
      <p>N: {number(coverage.members)} · {tx('纳入', 'Included')}: {number(coverage.included)} · {tx('排除', 'Excluded')}: {number(coverage.excluded)}</p><p>{tx('数据集计数（合格 / 无信号 / 未知）', 'Dataset counts (eligible / no signal / unknown)')}: {unknown}</p>
      <ul>{view.sources.map(({ source, row }) => <li key={source}><b>{({ hardware_fault_ledger: tx('故障确认记录', 'Confirmed-fault records'), identity: tx('身份归属记录', 'Identity records'), monitoring: tx('监控记录', 'Monitoring records') })[source]}</b>: {status(row?.check_status)} · {tx('缺成员覆盖记录', 'Missing member assertions')}: {number(row?.member_assertions_missing)} · {tx('缺历史候选', 'Missing historical candidates')}: {number(row?.cutoff_safe_history_missing)} · {tx('缺结果窗口记录', 'Missing outcome-window assertions')}: {number(row?.outcome_window_missing)}</li>)}</ul>
    </>}
    <details><summary>{tx('最近运营记录', 'Recent operation records')}</summary><p>{tx('最多展示最近62条记录，不代表全部积累天数。', 'At most 62 recent records; this is not the full accumulation duration.')}</p><ul>{view.records.slice(0, 62).map((r, i) => <li key={i}>{text(r.day) || unknown} · {r.stage === 'ranking' ? tx('排名', 'Ranking') : r.stage === 'outcomes' ? tx('结果', 'Outcomes') : unknown} · {status(r.status)} · Query #{number(r.query_id)}</li>)}</ul></details>
  </>;
}
export default function GPURiskOperations({ tx, refreshKey, queryID }: { tx: Tx; refreshKey: number; queryID?: number }) {
  const [load, setLoad] = useState<OperationsLoad>({ state: 'loading' });
  useEffect(() => loadOperations(setLoad), [refreshKey]);
  return <section className="gpu-risk-operations" aria-busy={load.state === 'loading'}><h3>{tx('积累运行状态', 'Accumulation operations')}</h3>
    {load.state === 'loading' ? <p role="status">{tx('正在读取运行状态…', 'Loading operations…')}</p> : load.state === 'error' ? <p role="alert">{tx('运行状态读取失败；榜单仍可独立查看，刷新页面数据可重试。', 'Operations unavailable; rankings remain independent. Refresh the page data to retry.')}</p> : <OperationsContent data={load.data} tx={tx} queryID={queryID} />}
  </section>;
}
