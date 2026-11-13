"""Pinned B1 ablation comparison only: no fitting, winner selection or admission."""
import argparse
import hashlib
import json
from pathlib import Path
from evaluate import evaluate, read_file, digest
from train_ablation import reload_scores
from feature_ablation import POLICIES as METHODS, filter_columns
from metrics import ranking_metrics


def compare(dataset, dataset_sha, artifacts, artifact_shas, split='validation', synthetic=False):
    original = evaluate(dataset, dataset_sha, split=split, synthetic=synthetic)
    ready = original['status'] == 'offline_artifact_comparison'
    if set(artifact_shas) != set(METHODS):
        raise ValueError('six artifact slots required')
    scores, metadata = {}, {}
    if ready:
        if set(artifacts) != set(METHODS):
            raise ValueError('six loaded artifacts required')
        reference = None
        for method in METHODS:
            artifact = artifacts[method]
            if artifact['wrapper']['manifest']['policy'] != method:
                raise ValueError('objective slot mismatch')
            scores[method] = reload_scores(artifact, dataset, dataset_sha)
            shared = {k: artifact[k] for k in ('binding','config','source_sha')}
            if reference is not None and digest(shared) != digest(reference):
                raise ValueError('incomparable configuration or preprocessing')
            reference = shared
            metadata[method] = {k: artifact[k] for k in ('objective','aggregation','config','selection','source_sha')}
            metadata[method].update(policy_manifest=artifact['wrapper']['manifest'],wrapper_sha=artifact['wrapper']['sha'],preprocessor_sha=digest(artifact['wrapper']['preprocessor']),input_dimension=len(artifact['layers'][0]['weights'][0]))
            expected=filter_columns(artifacts['core_all']['wrapper']['preprocessor'],artifact['wrapper']['manifest']['dropped'])
            if digest(expected)!=digest(artifact['wrapper']['preprocessor']):raise ValueError('retained preprocessing changed')
    indices = {id(row): i for i, row in enumerate(dataset['rows'])}
    queries = []
    names = ('uniform','episode_count',*METHODS)
    for source in original['queries']:
        rejected = {(r['node_id'],r['generation']) for r in source['row_rejections']}
        common = [r for r in dataset['rows'] if r['split']==split and r['query_id']==source['query_id'] and (r['node_id'],r['generation']) not in rejected]
        methods = {}
        for name in names:
            values = [0.0 if name=='uniform' else r['features']['numeric']['episode_count'] if name=='episode_count' else scores[name][indices[id(r)]] for r in common] if ready else []
            methods[name] = {'status':'computed' if ready else 'pending','reason':None if ready else 'dataset_not_ready','cohort_sha':source['cohort_sha'],'metrics':ranking_metrics(values,[r['label'] for r in common]) if ready else None}
        queries.append({'query_id':source['query_id'],'counts':source['counts'],'cohort_sha':source['cohort_sha'],'methods':methods})
    aggregate = {}
    for name in names:
        values = [q['methods'][name]['metrics']['auc'] for q in queries if q['methods'][name]['metrics'] is not None and q['methods'][name]['metrics']['auc'] is not None]
        mean = sum(values)/len(values) if values else None
        aggregate[name] = {'valid_query_count':len(values),'auc_mean':mean,'auc_population_variance':sum((x-mean)**2 for x in values)/len(values) if values else None}
    report = {'version':'gpu-ablation-comparison-v1','status':original['status'],'purpose':original['purpose'],'split':split,'input':original['input'],'artifacts':{m:{'requested_sha':artifact_shas[m],'verified_loaded_sha':artifact_shas[m] if ready else None,'loaded':ready} for m in METHODS},'candidate_metadata':metadata,'original_report_sha':original['sha'],'source_sha':{**original['source_sha'],'compare_ablations.py':hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),'train_ablation.py':hashlib.sha256(Path(__file__).with_name('train_ablation.py').read_bytes()).hexdigest()},'dataset_rows':original['dataset_rows'],'unique_node_generations':original['unique_node_generations'],'selected_split_rows':original['selected_split_rows'],'dataset_counts':original['dataset_counts'],'all_selected_positive_declared_episode_keys':original['all_selected_positive_declared_episode_keys'],'common_cohort_positive_declared_episode_keys':original['common_cohort_positive_declared_episode_keys'],'count_definitions':original['count_definitions'],'flags_may_overlap':True,'queries':queries,'aggregation':{'policy':'macro_non_null_query_AUC_not_independent_samples','methods':aggregate},'blockers':original['blockers'],'input_provenance':original['input_provenance'],'limitations':['pointwise_fixed_ablation_research','artifact_declarations_do_not_certify_training_history','test_metrics_must_not_select_configuration','episode_capture_not_certified','complete_B1_real_efficacy_pending','remaining_B0_B6_pending'],'probability_emitted':False,'no_action':True,'promoted':False,'sha':''}
    report['sha'] = digest(report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=('evaluate','self-test'))
    for name in ('dataset','dataset-sha','output'):
        parser.add_argument('--'+name,required=True)
    for method in METHODS:
        parser.add_argument('--'+method.replace('_','-')+'-file')
        parser.add_argument('--'+method.replace('_','-')+'-sha')
    parser.add_argument('--split',choices=('validation','test'),default='validation')
    args = parser.parse_args()
    try:
        if Path(args.output).exists():
            raise ValueError('output exists')
        data = read_file(args.dataset,args.dataset_sha)
        base = evaluate(data,args.dataset_sha,split=args.split,synthetic=args.command=='self-test')
        ready = base['status']=='offline_artifact_comparison'
        loaded, shas = {}, {}
        for method in METHODS:
            path, sha = getattr(args,method+'_file'),getattr(args,method+'_sha')
            if bool(path)!=bool(sha) or (ready and not path):
                raise ValueError('pinned artifact pair required')
            shas[method] = sha
            if ready:
                loaded[method] = read_file(path,sha)
        report = compare(data,args.dataset_sha,loaded,shas,args.split,args.command=='self-test')
        out=Path(args.output);out.mkdir(mode=0o700,parents=True,exist_ok=False)
        file=out/'report.json';file.write_text(json.dumps(report,sort_keys=True,separators=(',',':'),allow_nan=False)+'\n');file.chmod(0o600)
        print(json.dumps({'status':report['status'],'report_sha':report['sha'],'no_action':True}))
    except (ValueError,KeyError,TypeError,OSError,OverflowError,AttributeError,RuntimeError):
        parser.exit(2,'objective comparison rejected: invalid pinned inputs or comparison\n')


if __name__=='__main__':
    main()
