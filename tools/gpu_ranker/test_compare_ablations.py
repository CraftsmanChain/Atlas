import copy
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from compare_ablations import compare
from train_ablation import train_candidate
from feature_ablation import POLICIES as METHODS


class AblationComparisonTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        p=os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not p:raise unittest.SkipTest('Go fixture required')
        cls.dataset=json.loads(Path(p).read_bytes());cls.sha=hashlib.sha256(Path(p).read_bytes()).hexdigest()
        cls.artifacts={m:train_candidate(cls.dataset,cls.sha,m,epochs=1,synthetic=True)[0] for m in METHODS}
        cls.shas={m:hashlib.sha256(json.dumps(a).encode()).hexdigest() for m,a in cls.artifacts.items()}

    def test_same_cohort_determinism_and_no_fit(self):
        with patch('feature_ablation.fit',side_effect=AssertionError('fit forbidden')),patch('train_ablation.train_candidate',side_effect=AssertionError('training forbidden')):
            a=compare(self.dataset,self.sha,self.artifacts,self.shas,synthetic=True)
            self.assertEqual(a,compare(self.dataset,self.sha,self.artifacts,self.shas,synthetic=True))
        q=a['queries'][0];self.assertEqual(q['counts']['N'],25);self.assertEqual(q['counts']['eligible'],25)
        self.assertEqual({v['cohort_sha'] for v in q['methods'].values()},{q['cohort_sha']})
        self.assertEqual(set(q['methods']),{'uniform','episode_count',*METHODS})
        self.assertEqual(compare(self.dataset,self.sha,self.artifacts,self.shas,split='test',synthetic=True)['split'],'test')

    def test_comparability_rejection(self):
        for change in [lambda a:a['drop_calendar']['config'].update(seed=18),lambda a:a['drop_calendar']['config'].update(epochs=2),lambda a:a['drop_calendar']['wrapper']['manifest'].update(policy='core_all'),lambda a:a['drop_calendar']['wrapper']['preprocessor'].update(extra=True),lambda a:a['drop_calendar']['binding'].update(dataset_sha='0'*64)]:
            a=copy.deepcopy(self.artifacts);change(a)
            with self.assertRaises(ValueError):compare(self.dataset,self.sha,a,self.shas,synthetic=True)
        with self.assertRaises(ValueError):compare(self.dataset,self.sha,{},self.shas,synthetic=True)
        with self.assertRaises(ValueError):compare(self.dataset,self.sha,self.artifacts,self.shas)

    def test_blocked_full_denominator(self):
        d=copy.deepcopy(self.dataset);d['ready']=False;d['blockers']=['missing_source']
        d['rows'][70]['features']['history_unknown']=True
        r=compare(d,self.sha,{},self.shas,synthetic=True);self.assertEqual(r['queries'][0]['counts']['N'],25);self.assertEqual(r['queries'][0]['counts']['eligible'],24)
        self.assertTrue(all(x['verified_loaded_sha'] is None for x in r['artifacts'].values()))
        self.assertTrue(all(x['metrics'] is None for x in r['queries'][0]['methods'].values()))

    def test_single_class_query_null_metrics(self):
        d=copy.deepcopy(self.dataset)
        negative=copy.deepcopy(next(r for r in d['rows'] if r['split']=='validation' and r['label'] is False))
        negative['query_id']=99;d['rows'].append(negative)
        artifacts={m:train_candidate(d,self.sha,m,epochs=1,synthetic=True)[0] for m in METHODS}
        r=compare(d,self.sha,artifacts,self.shas,synthetic=True)
        q=next(q for q in r['queries'] if q['query_id']==99)
        self.assertEqual(q['counts']['N'],1)
        self.assertTrue(all(v['metrics']['auc'] is None for v in q['methods'].values()))
        self.assertTrue(all(v['metrics']['top_five_percent']['capture'] is None for v in q['methods'].values()))

    def test_cli_pinning_and_waiting_no_read(self):
        with tempfile.TemporaryDirectory() as td:
            p=Path(td);data=p/'data.json';data.write_bytes(Path(os.environ['ATLAS_GPU_RISK_MLP_FIXTURE']).read_bytes())
            args=[sys.executable,str(Path(__file__).with_name('compare_ablations.py')),'self-test','--dataset',str(data),'--dataset-sha',self.sha]
            for m in METHODS:
                f=p/(m+'.json');f.write_text(json.dumps(self.artifacts[m]));args+=['--'+m.replace('_','-')+'-file',str(f),'--'+m.replace('_','-')+'-sha',hashlib.sha256(f.read_bytes()).hexdigest()]
            for n in ('a','b'):
                cp=subprocess.run(args+['--output',str(p/n)],capture_output=True);self.assertEqual(cp.returncode,0,cp.stderr)
            self.assertEqual((p/'a/report.json').read_bytes(),(p/'b/report.json').read_bytes())
            bad=args.copy();bad[bad.index('--drop-calendar-sha')+1]='0'*64
            self.assertEqual(subprocess.run(bad+['--output',str(p/'bad')],capture_output=True).returncode,2)
            d=copy.deepcopy(self.dataset);d['ready']=False;d['blockers']=['missing_source'];data.write_text(json.dumps(d));args[args.index('--dataset-sha')+1]=hashlib.sha256(data.read_bytes()).hexdigest()
            for m in METHODS:(p/(m+'.json')).unlink()
            cp=subprocess.run(args+['--output',str(p/'waiting')],capture_output=True);self.assertEqual(cp.returncode,0,cp.stderr)

    def test_retained_statistics_resealed_rejected(self):
        from evaluate import digest
        a=copy.deepcopy(self.artifacts)
        wrapper=a['drop_calendar']['wrapper'];wrapper['preprocessor']['numeric'][0]['mean']+=1
        wrapper['sha']='';wrapper['sha']=digest(wrapper)
        with self.assertRaisesRegex(ValueError,'retained preprocessing'):
            compare(self.dataset,self.sha,a,self.shas,synthetic=True)
