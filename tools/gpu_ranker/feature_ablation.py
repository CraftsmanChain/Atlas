"""Independent B1 preprocessing research policies; no training or admission."""
import hashlib
import math
from pathlib import Path
from evaluate import basic, digest
from train import validate
from preprocess import fit, transform, NUMERIC, CATEGORIES, MAPS
from feature_profile import SCHEMA_SHA

GROUPS = {'composition':['episode_count','unresolved_count',*MAPS],
          'recurrence':['episode_count_30d','mean_interval_days','frequency_per_day','recent_ratio'],
          'calendar':['weekday','weekend'], 'host_context':list(CATEGORIES)}
POLICIES = ('core_all','drop_composition','drop_recurrence','drop_calendar','drop_host_context','drop_host_id')
SOURCES = ('feature_ablation.py','preprocess.py','feature_profile.py','train.py','evaluate.py','requirements.lock')


def sources():
    return {n:hashlib.sha256(Path(__file__).with_name(n).read_bytes()).hexdigest() for n in SOURCES}


def manifest(dataset, dataset_sha, policy):
    basic(dataset)
    if policy not in POLICIES or not isinstance(dataset_sha,str) or len(dataset_sha)!=64 or any(c not in '0123456789abcdef' for c in dataset_sha):
        raise ValueError('invalid policy or file fingerprint')
    dropped=[] if policy=='core_all' else ['host_id'] if policy=='drop_host_id' else GROUPS[policy[5:]]
    fields=[*NUMERIC,*MAPS,*CATEGORIES]
    m=dict(version='gpu-feature-ablation-policy-v1',policy=policy,dataset_sha=dataset_sha,dataset_manifest_sha=dataset['sha'],feature_sha=dataset['feature_sha'],split_sha=dataset['split_sha'],input_sha=dataset['input_sha'],profile=dataset['spec']['profile'],feature_contract=dataset['feature_contract'],schema_sha=SCHEMA_SHA,groups={k:list(v) for k,v in GROUPS.items()},retained=[n for n in fields if n not in dropped],dropped=list(dropped),scope='independent_research_transform_not_core_manifest',sha='')
    m['sha']=digest(m)
    return m


def filter_columns(prep, dropped):
    return {'numeric':[c for c in prep['numeric'] if (c['name'] if c['group']=='numeric' else c['group']) not in dropped],
            'categories':[c for c in prep['categories'] if c['name'] not in dropped]}


def fit_policy(dataset, dataset_sha, policy, synthetic=False):
    m=manifest(dataset,dataset_sha,policy)
    groups=validate(dataset,synthetic)
    prep=filter_columns(fit(groups['train']),m['dropped'])
    result=dict(version='gpu-feature-ablation-preprocessor-v1',manifest=m,source_sha=sources(),preprocessor=prep,fit_semantics='all_train_columns_fit_then_removed_whole_families; no_validation_test_fit',sha='')
    result['sha']=digest(result)
    return result


def validate_wrapper(wrapper, dataset, dataset_sha):
    if not isinstance(wrapper,dict) or set(wrapper)!={'version','manifest','source_sha','preprocessor','fit_semantics','sha'}:
        raise ValueError('closed wrapper schema')
    if wrapper['version']!='gpu-feature-ablation-preprocessor-v1' or wrapper['manifest']!=manifest(dataset,dataset_sha,wrapper['manifest']['policy']) or wrapper['source_sha']!=sources() or wrapper['fit_semantics']!='all_train_columns_fit_then_removed_whole_families; no_validation_test_fit':
        raise ValueError('wrapper binding')
    unsigned={**wrapper,'sha':''}
    if wrapper['sha']!=digest(unsigned):raise ValueError('wrapper fingerprint')
    p=wrapper['preprocessor'];dropped=wrapper['manifest']['dropped']
    if not isinstance(p,dict) or set(p)!={'numeric','categories'} or not isinstance(p['numeric'],list) or not isinstance(p['categories'],list):raise ValueError('preprocessor schema')
    expected=[]
    for n in NUMERIC:
        if n not in dropped:expected.append(('numeric',n,False,[]))
    for group in MAPS:
        if group in dropped:continue
        cols=[c for c in p['numeric'] if c.get('group')==group]
        if not cols:raise ValueError('missing count family')
        keys=cols[-1].get('known_keys')
        if not isinstance(keys,list) or any(not isinstance(k,str) or len(k)>256 for k in keys) or keys!=sorted(set(keys)) or len(keys)>128:raise ValueError('map vocabulary')
        expected += [(group,k,False,keys) for k in keys]+[(group,'',True,keys)]
    if len(expected)!=len(p['numeric']):raise ValueError('numeric columns')
    for c,(group,name,oov,keys) in zip(p['numeric'],expected):
        if set(c)!={'group','name','oov','known_keys','mean','scale'} or c['group']!=group or c['name']!=name or type(c['oov']) is not bool or c['oov']!=oov or c['known_keys']!=keys:raise ValueError('numeric column policy')
        for field in ('mean','scale'):
            if type(c[field]) not in (int,float) or not math.isfinite(c[field]):raise ValueError('numeric statistics')
        if c['scale']<=0:raise ValueError('numeric scale')
    names=[n for n in CATEGORIES if n not in dropped]
    if len(names)!=len(p['categories']):raise ValueError('category columns')
    for c,name in zip(p['categories'],names):
        if set(c)!={'name','vocab'} or c['name']!=name:raise ValueError('category policy')
        v=c['vocab']
        if not isinstance(v,list) or any(not isinstance(x,str) or len(x)>256 for x in v) or v!=sorted(set(v)) or len(v)>1024:raise ValueError('category vocabulary')
    return p


def transform_policy(wrapper, dataset, rows, dataset_sha=None):
    """Caller byte SHA binds files; it does not certify historical fit or source."""
    p=validate_wrapper(wrapper,dataset,dataset_sha if dataset_sha is not None else wrapper['manifest']['dataset_sha'])
    return transform(p,rows)
