"""Offline objective research candidates; incompatible with production registry."""
import argparse
import copy
import hashlib
import json
import math
from pathlib import Path
import torch
from evaluate import basic, read_file
from train import validate, SYNTHETIC
from preprocess import fit, transform
from model import Ranker
from objectives import objective, VERSION as OBJECTIVE_VERSION

VERSION = 'gpu-objective-research-artifact-v1'
METHODS = ('pointwise', 'listnet', 'pairwise')
SOURCES = ('train_objective.py','train.py','objectives.py','model.py','preprocess.py','feature_profile.py','requirements.lock')


def loss_for(method, z, rows):
    result = objective(method, z, torch.tensor([r['label'] for r in rows]), [r['query_id'] for r in rows])
    if result['loss'] is None:
        raise ValueError('undefined objective: no pairs')
    return result


def binding(d, sha):
    return dict(dataset_sha=sha, dataset_manifest_sha=d['sha'], feature_sha=d['feature_sha'], split_sha=d['split_sha'], input_sha=d['input_sha'], target=d['target'], profile=d['spec']['profile'], feature_contract=d['feature_contract'])


def reload_scores(artifact, d, sha):
    if set(artifact) != {'version','binding','purpose','objective','objective_version','aggregation','diagnostics','preprocessor','layers','config','source_sha','selection','probability_emitted','no_action','promoted'}:raise ValueError('closed research schema')
    if artifact['source_sha'] != {n:hashlib.sha256(Path(__file__).with_name(n).read_bytes()).hexdigest() for n in SOURCES}:raise ValueError('source binding')
    basic(d);groups=validate(d, artifact['purpose'] == SYNTHETIC)
    method=artifact['objective']
    expected_aggregation={'pointwise':'row_mean','listnet':'query_macro_mean_softmax_binary_labels','pairwise':'valid_query_macro_mean_all_positive_negative_pairs'}
    if method not in METHODS or artifact['aggregation']!=expected_aggregation[method]:raise ValueError('aggregation mismatch')
    config=artifact['config']
    if not isinstance(config,dict) or set(config)!={'seed','epochs','device','dtype','optimizer','lr','batch','patience'}:raise ValueError('closed config')
    if type(config['seed']) is not int or not 0<=config['seed']<=2**31-1 or type(config['epochs']) is not int or not 1<=config['epochs']<=100:raise ValueError('config budget')
    if {k:v for k,v in config.items() if k not in ('seed','epochs')}!=dict(device='cpu',dtype='float32',optimizer='Adam',lr=1e-4,batch='full_train_rows',patience=10):raise ValueError('config policy')
    selection=artifact['selection']
    if not isinstance(selection,dict) or set(selection)!={'validation_loss','best_epoch','test_used_for_selection'} or type(selection['best_epoch']) is not int or not 1<=selection['best_epoch']<=config['epochs'] or type(selection['validation_loss']) not in (int,float) or not math.isfinite(selection['validation_loss']) or selection['validation_loss']<0 or selection['test_used_for_selection'] is not False:raise ValueError('selection contract')
    expected_diagnostics={}
    for split in ('train','validation'):
        pre=loss_for(method,torch.zeros(len(groups[split])),groups[split]);expected_diagnostics[split]={k:v for k,v in pre.items() if k!='loss'}
    if artifact['diagnostics']!=expected_diagnostics:raise ValueError('diagnostics mismatch')
    for split,expected in expected_diagnostics.items():
        if any(type(artifact['diagnostics'][split][k]) is not type(v) for k,v in expected.items()):raise ValueError('diagnostic types')
    if artifact['version'] != VERSION or artifact['binding'] != binding(d,sha) or artifact['objective'] not in METHODS or artifact['objective_version'] != OBJECTIVE_VERSION or artifact['selection']['test_used_for_selection'] is not False or artifact['probability_emitted'] is not False or artifact['no_action'] is not True or artifact['promoted'] is not False:
        raise ValueError('research artifact contract')
    if artifact['purpose'] not in (SYNTHETIC,'offline_objective_research_not_promoted'):
        raise ValueError('research purpose')
    matrix=torch.tensor(transform(artifact['preprocessor'],d['rows']),dtype=torch.float32)
    model=Ranker(matrix.shape[1]).float().cpu()
    if len(artifact['layers']) != 4:raise ValueError('layers')
    with torch.no_grad():
        for pos,layer in zip((0,3,6,9),artifact['layers']):
            if set(layer) != ({'weights','bias','bn'} if pos<9 else {'weights','bias'}):raise ValueError('closed layer')
            pairs=[(model.layers[pos].weight,layer['weights']),(model.layers[pos].bias,layer['bias'])]
            if pos<9:
                bn=model.layers[pos+1];b=layer['bn']
                if set(b)!={'mean','variance','gamma','beta','eps'} or b['eps']!=bn.eps:raise ValueError('BN contract')
                pairs += [(bn.running_mean,b['mean']),(bn.running_var,b['variance']),(bn.weight,b['gamma']),(bn.bias,b['beta'])]
                if any(x<0 for x in b['variance']):raise ValueError('negative variance')
            for target,value in pairs:
                tensor=torch.tensor(value,dtype=torch.float32)
                if tensor.shape!=target.shape or not torch.isfinite(tensor).all():raise ValueError('tensor shape/value')
                target.copy_(tensor)
    model.eval()
    with torch.no_grad():scores=model(matrix).tolist()
    if not all(math.isfinite(x) for x in scores):raise ValueError('nonfinite scores')
    return scores


def train_candidate(d, sha, method, seed=17, epochs=100, synthetic=False):
    basic(d);groups=validate(d,synthetic)
    if method not in METHODS or type(seed) is not int or not 0<=seed<=2**31-1 or type(epochs) is not int or not 1<=epochs<=100:raise ValueError('config budget')
    diagnostics={}
    for split in ('train','validation'):
        pre=loss_for(method,torch.zeros(len(groups[split])),groups[split]);diagnostics[split]={k:v for k,v in pre.items() if k!='loss'}
    torch.set_num_threads(1);torch.use_deterministic_algorithms(True);torch.manual_seed(seed)
    prep=fit(groups['train'])
    x=torch.tensor(transform(prep,groups['train']),dtype=torch.float32);vx=torch.tensor(transform(prep,groups['validation']),dtype=torch.float32)
    model=Ranker(x.shape[1]).float().cpu();optimizer=torch.optim.Adam(model.parameters(),lr=1e-4)
    best,stale,best_epoch,checkpoint=math.inf,0,0,None
    for epoch in range(epochs):
        model.train();optimizer.zero_grad();loss=loss_for(method,model(x),groups['train'])['loss'];loss.backward();optimizer.step();model.eval()
        with torch.no_grad():value=loss_for(method,model(vx),groups['validation'])['loss'].item()
        if not math.isfinite(value):raise ValueError('validation loss')
        if value<best:best,stale,best_epoch,checkpoint=value,0,epoch+1,copy.deepcopy(model.state_dict())
        else:stale+=1
        if stale>=10:break
    model.load_state_dict(checkpoint);model.eval()
    with torch.no_grad():scores=model(torch.tensor(transform(prep,d['rows']),dtype=torch.float32)).tolist()
    artifact=dict(version=VERSION,binding=binding(d,sha),purpose=SYNTHETIC if synthetic else 'offline_objective_research_not_promoted',objective=method,objective_version=OBJECTIVE_VERSION,aggregation={'pointwise':'row_mean','listnet':'query_macro_mean_softmax_binary_labels','pairwise':'valid_query_macro_mean_all_positive_negative_pairs'}[method],diagnostics=diagnostics,preprocessor=prep,layers=model.export(),config=dict(seed=seed,epochs=epochs,device='cpu',dtype='float32',optimizer='Adam',lr=1e-4,batch='full_train_rows',patience=10),source_sha={n:hashlib.sha256(Path(__file__).with_name(n).read_bytes()).hexdigest() for n in SOURCES},selection=dict(validation_loss=best,best_epoch=best_epoch,test_used_for_selection=False),probability_emitted=False,no_action=True,promoted=False)
    reloaded=reload_scores(artifact,d,sha)
    if any(abs(a-b)>1e-6+1e-5*abs(a) for a,b in zip(scores,reloaded)):raise ValueError('reload parity')
    return artifact,reloaded


def write_json(path,value):
    raw=(json.dumps(value,sort_keys=True,separators=(',',':'),allow_nan=False)+'\n').encode();path.write_bytes(raw);path.chmod(0o600);return hashlib.sha256(raw).hexdigest()


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('command',choices=['train','self-test','score'])
    for name in ('dataset','dataset-sha','output'):p.add_argument('--'+name,required=True)
    p.add_argument('--objective',choices=METHODS);p.add_argument('--seed',type=int,default=17);p.add_argument('--epochs',type=int,default=100);p.add_argument('--artifact-file');p.add_argument('--artifact-sha');a=p.parse_args()
    try:
        if Path(a.output).exists():raise ValueError('output exists')
        d=read_file(a.dataset,a.dataset_sha)
        if a.command=='score':
            if not a.artifact_file or not a.artifact_sha:raise ValueError('pinned artifact required')
            artifact=read_file(a.artifact_file,a.artifact_sha);scores=reload_scores(artifact,d,a.dataset_sha);artifact_sha=a.artifact_sha
        else:
            if not a.objective:raise ValueError('explicit objective required')
            artifact,scores=train_candidate(d,a.dataset_sha,a.objective,a.seed,a.epochs,a.command=='self-test');artifact_sha=None
        out=Path(a.output);out.mkdir(mode=0o700,parents=True,exist_ok=False)
        if artifact_sha is None:artifact_sha=write_json(out/'model.json',artifact)
        raw=dict(version='gpu-objective-research-raw-scores-v1',artifact_sha=artifact_sha,rows=[dict(query_id=r['query_id'],node_id=r['node_id'],generation=r['generation'],raw_score=z) for r,z in zip(d['rows'],scores)])
        scores_sha=write_json(out/'raw-scores.json',raw)
        write_json(out/'manifest.json',dict(version=VERSION,dataset_sha=a.dataset_sha,artifact_sha=artifact_sha,files={'raw-scores.json':scores_sha,**({'model.json':artifact_sha} if a.command!='score' else {})},probability_emitted=False,no_action=True,promoted=False))
        print(json.dumps({'status':'offline_research_only','artifact_sha':artifact_sha}))
    except (ValueError,KeyError,TypeError,OSError,OverflowError,AttributeError,RuntimeError):p.exit(2,'objective research rejected: invalid pinned input or candidate\n')
if __name__=='__main__':main()
