"""Pinned artifact evaluation only. No fitting, database, network or promotion."""
import argparse
import hashlib
import json
import math
from datetime import datetime, timedelta
from pathlib import Path
import feature_profile as fp
from preprocess import NUMERIC, CATEGORIES, MAPS
from train import validate, score_artifact, SYNTHETIC
from metrics import ranking_metrics


def digest(v):
    return hashlib.sha256(json.dumps(v, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def read_file(path, sha):
    with Path(path).open('rb') as stream:
        raw = stream.read(64*1024*1024+1)
    if len(raw)>64*1024*1024 or hashlib.sha256(raw).hexdigest()!=sha:
        raise ValueError('pinned file SHA or budget mismatch')
    def pairs(items):
        d = {}
        for k,v in items:
            if k in d: raise ValueError('duplicate JSON key')
            d[k]=v
        return d
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError('nonfinite JSON')))


def basic(d):
    if not fp.valid_dataset(d) or d['target']!='confirmed_hardware_failure': raise ValueError('dataset tuple mismatch')
    if fp.digest(d['schema'])!=fp.SCHEMA_SHA: raise ValueError('schema mismatch')
    for k in ('ready','exploration_ready','paper_core_complete'):
        if type(d[k]) is not bool: raise ValueError('invalid readiness type')
    for k in ('sha','feature_sha','split_sha','input_sha'):
        if not isinstance(d[k],str) or len(d[k])!=64 or any(c not in '0123456789abcdef' for c in d[k]): raise ValueError('invalid digest')
    if not isinstance(d['blockers'],list) or any(not isinstance(b,str) for b in d['blockers']): raise ValueError('invalid blockers')
    if not isinstance(d['implementation_choices'],list) or any(not isinstance(x,str) for x in d['implementation_choices']): raise ValueError('invalid implementation choices')
    if not isinstance(d['counts'],dict) or any(not isinstance(k,str) or type(v) is not int or v<0 for k,v in d['counts'].items()): raise ValueError('invalid dataset counts')
    rows=d['rows']
    if not isinstance(rows,list) or len(rows)>10000: raise ValueError('row budget')
    dates=[datetime.fromisoformat(d['spec'][k].replace('Z','+00:00')) for k in ('anchor','train_end','validation_end','test_end')]
    if not all(x.tzinfo for x in dates) or not all(a<b for a,b in zip(dates,dates[1:])): raise ValueError('invalid temporal boundaries')
    seen=set(); query_times={}
    for r in rows:
        if any(type(r[k]) is not int or r[k]<(0 if k=='generation' else 1) for k in ('query_id','node_id','generation','batch_id')): raise ValueError('invalid row identity')
        key=(r['query_id'],r['node_id'])
        if key in seen: raise ValueError('duplicate row')
        seen.add(key)
        if type(r['eligible']) is not bool or (r['label'] is not None and type(r['label']) is not bool) or r['split'] not in ('train','validation','test'): raise ValueError('invalid row type')
        for bs in (r['blockers'],r['features']['blockers'],r['episode_keys']):
            if not isinstance(bs,list) or any(not isinstance(x,str) for x in bs): raise ValueError('invalid list')
        at=datetime.fromisoformat(r['cutoff'].replace('Z','+00:00'))
        if not at.tzinfo or query_times.setdefault(r['query_id'],at)!=at: raise ValueError('query cutoff mismatch')
        expected='train' if at<dates[1] else 'validation' if at<dates[2] else 'test'
        if r['split']!=expected: raise ValueError('row split time mismatch')
        if not isinstance(r['entity_ref'],str) or not isinstance(r['component'],str): raise ValueError('invalid entity')
        if r['eligible'] != (len(r['blockers'])==0): raise ValueError('eligible/blockers conflict')
        boundary=dates[1] if expected=='train' else dates[2] if expected=='validation' else dates[3]
        lower=dates[0] if expected=='train' else dates[1] if expected=='validation' else dates[2]
        if r['eligible'] and (type(r['label']) is not bool or not r['entity_ref'] or not r['component'] or at<lower or at+timedelta(days=8)>boundary): raise ValueError('eligible temporal/identity/label conflict')
        f=r['features']
        if not isinstance(f,dict) or not isinstance(f['numeric'],dict) or not isinstance(f['categorical'],dict): raise ValueError('invalid feature containers')
        if type(f['history_unknown']) is not bool: raise ValueError('invalid history flag')
        if set(f['numeric'])!=set(NUMERIC) or set(f['categorical'])!=set(CATEGORIES): raise ValueError('feature columns')
        for v in f['numeric'].values():
            if v is not None and (type(v) not in (int,float) or not math.isfinite(v)): raise ValueError('invalid numeric')
        if f['numeric']['episode_count'] is not None and f['numeric']['episode_count']<0: raise ValueError('negative episode count')
        for v in f['categorical'].values():
            if v is not None and (not isinstance(v,str) or len(v)>256): raise ValueError('invalid category')
        for k in MAPS:
            if f[k] is not None and (not isinstance(f[k],dict) or any(not isinstance(k,str) or type(v) is not int or v<0 for k,v in f[k].items())): raise ValueError('invalid count map')
    return rows


def evaluate(d, dataset_sha, split='validation', artifact=None, artifact_sha=None, synthetic=False):
    if split not in ('validation','test'): raise ValueError('unsupported evaluation split')
    rows=basic(d)
    if (SYNTHETIC in d['implementation_choices'])!=synthetic: raise ValueError('synthetic requires self-test')
    selected=[r for r in rows if r['split']==split]
    ready=d['ready'] and d['exploration_ready'] and fp.feature_ready(d) and not d['blockers']
    logits=None
    if ready:
        validate(d,synthetic)
        if artifact is not None:
            selection=artifact.get('selection')
            if not isinstance(selection,dict) or selection.get('test_used_for_selection') is not False:
                raise ValueError('checkpoint test-selection declaration rejected')
            logits=score_artifact(artifact,d,dataset_sha)
    sources={n:hashlib.sha256(Path(__file__).with_name(n).read_bytes()).hexdigest() for n in ('evaluate.py','metrics.py','train.py','feature_profile.py','preprocess.py','model.py')}
    report={'version':'gpu-ranking-comparison-v1','status':'offline_artifact_comparison' if ready else 'waiting_for_data','purpose':SYNTHETIC if synthetic else 'offline_exploration_not_promoted','split':split,'input':{'dataset_sha':dataset_sha,'dataset_manifest_sha':d['sha'],'feature_sha':d['feature_sha'],'split_sha':d['split_sha'],'input_sha':d['input_sha'],'requested_artifact_sha':artifact_sha,'verified_loaded_artifact_sha':artifact_sha if logits is not None else None,'artifact_loaded_and_scored':logits is not None},'source_sha':sources,'input_provenance':'caller_pinned_files_not_live_database_E1_certification','probability_emitted':False,'no_action':True,'promoted':False,'dataset_counts':d['counts'],'dataset_rows':len(rows),'unique_node_generations':len({(r['node_id'],r['generation']) for r in rows}),'selected_split_rows':len(selected),'all_selected_positive_declared_episode_keys':len({e for r in selected if r['label'] is True for e in r['episode_keys']}),'common_cohort_positive_declared_episode_keys':0,'episode_key_semantics':'deduplicates_declared_canonical_keys_only_not_independent_incident_certification','count_definitions':{'eligible':'common_candidate_rows_not_E1_gate_pass','method_no_signal':'episode_count_missing_or_history_unknown','unknown':'label_unknown_or_required_feature_blocked_or_history_unknown','excluded':'dataset_ineligible_or_row_blockers','N':'all_rows_for_selected_split_query'},'flags_may_overlap':True,'health_no_signal':None,'health_no_signal_reason':'not_available_in_dataset_rows','blockers':d['blockers'],'queries':[],'pending_methods':{'health_inverse':'missing_pinned_historical_health_scores','weighted_count':'weights_not_frozen','other_B0_B6':'not_implemented_in_first_slice'},'episode_capture':{'status':'pending','reason':'node_query_metrics_not_independent_episode_capture'},'sha':''}
    indexed={id(r):i for i,r in enumerate(rows)}
    common_episode_keys=set()
    for query in sorted({r['query_id'] for r in selected}):
        group=[r for r in selected if r['query_id']==query]
        blocked=lambda r: (fp.blocked(r['features']['blockers']) if fp.production(d) else bool(r['features']['blockers']))
        missing=lambda r:r['features']['numeric']['episode_count'] is None or r['features']['history_unknown']
        common=[r for r in group if r['eligible'] and not r['blockers'] and type(r['label']) is bool and not blocked(r) and not missing(r)]
        common_episode_keys.update(e for r in common if r['label'] is True for e in r['episode_keys'])
        counts={'N':len(group),'row_eligible':sum(r['eligible'] for r in group),'eligible':len(common),'excluded':sum(not r['eligible'] or bool(r['blockers']) for r in group),'unknown':sum(r['label'] is None or blocked(r) or r['features']['history_unknown'] for r in group),'history_unknown':sum(r['features']['history_unknown'] for r in group),'label_unknown':sum(r['label'] is None for r in group),'required_feature_blocked':sum(blocked(r) for r in group),'method_no_signal':sum(missing(r) for r in group),'no_signal':None}
        methods={}
        for name in ('uniform','episode_count','mlp'):
            reason='dataset_not_ready' if not ready else 'missing_pinned_artifact' if name=='mlp' and logits is None else None
            scores=[0.0 if name=='uniform' else r['features']['numeric']['episode_count'] if name=='episode_count' else logits[indexed[id(r)]] for r in common] if reason is None else []
            methods[name]={'status':'pending' if reason else 'computed','reason':reason,'metrics':None if reason else ranking_metrics(scores,[r['label'] for r in common])}
        report['queries'].append({'query_id':query,'counts':counts,'cohort_sha':digest([(r['query_id'],r['node_id'],r['generation']) for r in common]),'row_rejections':[{'node_id':r['node_id'],'generation':r['generation'],'blockers':r['blockers'],'feature_blockers':r['features']['blockers']} for r in group if r not in common],'methods':methods})
    report['common_cohort_positive_declared_episode_keys']=len(common_episode_keys)
    report['aggregation']={'policy':'macro_mean_of_non_null_per_query_AUC','methods':{}}
    for method in ('uniform','episode_count','mlp'):
        vals=[q['methods'][method]['metrics']['auc'] for q in report['queries'] if q['methods'][method]['metrics'] is not None and q['methods'][method]['metrics']['auc'] is not None]
        report['aggregation']['methods'][method]={'auc_mean':sum(vals)/len(vals) if vals else None,'valid_query_count':len(vals)}
    report['sha']=digest(report)
    return report


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('command',choices=['evaluate','self-test']);p.add_argument('--dataset',required=True);p.add_argument('--dataset-sha',required=True);p.add_argument('--artifact-file');p.add_argument('--artifact-sha');p.add_argument('--split',choices=['validation','test'],default='validation');p.add_argument('--output',required=True);a=p.parse_args()
    try:
        if bool(a.artifact_file)!=bool(a.artifact_sha): raise ValueError('artifact path and SHA required together')
        d=read_file(a.dataset,a.dataset_sha);basic(d)
        ready=d['ready'] and d['exploration_ready'] and fp.feature_ready(d) and not d['blockers']
        artifact=read_file(a.artifact_file,a.artifact_sha) if a.artifact_file and ready else None
        report=evaluate(d,a.dataset_sha,a.split,artifact,a.artifact_sha,a.command=='self-test');out=Path(a.output);out.mkdir(mode=0o700,exist_ok=False,parents=True);f=out/'report.json';f.write_text(json.dumps(report,sort_keys=True,separators=(',',':'),allow_nan=False)+'\n');f.chmod(0o600);print(json.dumps({'status':report['status'],'report_sha':report['sha'],'no_action':True}))
    except (ValueError,KeyError,TypeError,OSError):p.exit(2,'evaluation rejected: invalid input, readiness or pinned file\n')
if __name__=='__main__':main()
