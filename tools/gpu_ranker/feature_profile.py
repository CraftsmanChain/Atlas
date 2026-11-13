"""Closed profile tuples; absence is distinct from conflict and unknown policy."""
import hashlib
import json
OPTIONAL = ['host_context_unknown:' + n for n in ('host_type','gpu_model','manufacturer','main_board','quota_group','data_center','rack')]
SCHEMA_SHA = 'a048856601f8c048229e770901caba0408a500a893e5d2972353869c3e488904'
PROFILE = 'atlas_available_context_v1'
FEATURE = 'gpu-host-history-available-context-v1'
ARTIFACT = 'gpu-host-mlp-artifact-v2'
RANKER = 'hearank-atlas-available-context-v1'
def digest(v):
    return hashlib.sha256(json.dumps(v,ensure_ascii=False,separators=(',',':')).encode()).hexdigest()
def manifest(schema):
    m = dict(version='gpu-context-availability-policy-v1',schema_sha=digest(schema),optional_blockers=OPTIONAL,missing='null_missing_channel_separate_from_oov_train_only_vocabulary',sha='')
    m['sha']=digest(m)
    return m
def production(d):
    return d['spec']['profile']==PROFILE
def valid_dataset(d):
    if production(d):
        return digest(d['schema'])==SCHEMA_SHA and d['version']=='gpu-host-history-dataset-v3' and d['feature_contract']==FEATURE and d.get('availability_manifest')==manifest(d['schema']) and type(d.get('profile_feature_complete')) is bool
    return d['spec']['profile']=='paper_core' and d['version']=='gpu-host-history-dataset-v2' and d['feature_contract']=='gpu-host-history-v2' and 'availability_manifest' not in d and 'profile_feature_complete' not in d
def feature_ready(d):
    return d['profile_feature_complete'] if production(d) else d['paper_core_complete']
def blocked(bs):
    return any(b not in OPTIONAL for b in bs)
