import copy
import json
import os
import unittest
from pathlib import Path
from unittest.mock import patch
from evaluate import digest
from preprocess import fit,transform
from feature_ablation import manifest,fit_policy,transform_policy,POLICIES

class AblationTests(unittest.TestCase):
    def data(self):
        p=os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not p:self.skipTest('Go fixture required')
        return json.loads(Path(p).read_bytes())

    def test_six_policies_and_core_compatibility(self):
        d=self.data();train=[r for r in d['rows'] if r['split']=='train'];original=fit(train)
        for policy in POLICIES:
            m=manifest(d,'a'*64,policy);self.assertEqual(m,manifest(d,'a'*64,policy))
            w=fit_policy(d,'a'*64,policy,True);values=transform_policy(w,d,d['rows'])
            self.assertTrue(values);self.assertLessEqual(len(values[0]),len(transform(original,train)[0]))
            if policy=='core_all':self.assertEqual(w['preprocessor'],original);self.assertEqual(values,transform(original,d['rows']))
            for c in w['preprocessor']['numeric']:
                self.assertNotIn(c['name'] if c['group']=='numeric' else c['group'],m['dropped']);self.assertIn(c,original['numeric'])
            for c in w['preprocessor']['categories']:self.assertNotIn(c['name'],m['dropped']);self.assertIn(c,original['categories'])
        with self.assertRaises(ValueError):manifest(d,'a'*64,'unknown')
        with self.assertRaises(ValueError):manifest(d,'bad','core_all')

    def test_manifest_result_cannot_mutate_policy_registry(self):
        d=self.data();before=manifest(d,'a'*64,'drop_calendar')
        changed=manifest(d,'a'*64,'drop_calendar')
        changed['groups']['calendar'].append('episode_count')
        changed['dropped'].append('episode_count')
        self.assertEqual(manifest(d,'a'*64,'drop_calendar'),before)
        w=fit_policy(d,'a'*64,'drop_calendar',True)
        self.assertEqual(w['manifest'],before)
        w['manifest']['groups']['calendar'].append('episode_count')
        w['sha']='';w['sha']=digest(w)
        with self.assertRaises(ValueError):transform_policy(w,d,d['rows'])

    def test_removed_values_missing_and_oov_invariant(self):
        d=self.data()
        for policy in POLICIES[1:]:
            w=fit_policy(d,'a'*64,policy,True);rows=copy.deepcopy(d['rows'])
            for r in rows:
                f=r['features']
                for name in w['manifest']['dropped']:
                    if name in f['numeric']:f['numeric'][name]=None
                    elif name in f['categorical']:f['categorical'][name]='never-seen-removed-category'
                    else:f[name]={'never-seen-key':999999}
            self.assertEqual(transform_policy(w,d,rows),transform_policy(w,d,d['rows']))

    def test_train_only_and_heldout_changes(self):
        import feature_ablation
        d=self.data();seen=[];old=feature_ablation.fit
        with patch('feature_ablation.fit',side_effect=lambda rows:(seen.extend(r['split'] for r in rows),old(rows))[1]):a=fit_policy(d,'a'*64,'core_all',True)
        self.assertEqual(set(seen),{'train'})
        changed=copy.deepcopy(d)
        for r in changed['rows']:
            if r['split']!='train':r['features']['numeric']['frequency_per_day']=999999;r['features']['categorical']['rack']='heldout-only'
        b=fit_policy(changed,'b'*64,'core_all',True);self.assertEqual(a['preprocessor'],b['preprocessor'])
        d['ready']=False
        with patch('feature_ablation.fit',side_effect=AssertionError('must not fit')):
            with self.assertRaises(ValueError):fit_policy(d,'a'*64,'core_all',True)

    def test_tampering_rejected_without_refit(self):
        d=self.data();w=fit_policy(d,'a'*64,'drop_calendar',True)
        def fingerprint(x):x['sha']='';x['sha']=digest(x)
        for change in [lambda x:x['preprocessor']['numeric'].append({'group':'numeric','name':'weekday','oov':False,'known_keys':[],'mean':0,'scale':1}),lambda x:x['preprocessor']['numeric'][0].update(scale=-1),lambda x:x['manifest'].update(policy='core_all'),lambda x:x['source_sha'].update(extra='0'*64)]:
            x=copy.deepcopy(w);change(x);fingerprint(x)
            with self.assertRaises(ValueError):transform_policy(x,d,d['rows'])
        with self.assertRaises(ValueError):transform_policy(w,d,d['rows'],dataset_sha='b'*64)
        with patch('feature_ablation.fit',side_effect=AssertionError('no refit')):transform_policy(w,d,d['rows'])

    def test_available_profile_isolation(self):
        p=os.environ.get('ATLAS_AVAILABLE_CONTEXT_FIXTURE')
        if not p:self.skipTest('Available fixture required')
        av=json.loads(Path(p).read_bytes());w=fit_policy(av,'b'*64,'drop_host_context',True)
        self.assertEqual(w['preprocessor']['categories'],[])
        with self.assertRaises(ValueError):transform_policy(w,self.data(),self.data()['rows'])
