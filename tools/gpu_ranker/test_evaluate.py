import copy
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from metrics import ranking_metrics
from evaluate import evaluate, read_file

class EvaluationTests(unittest.TestCase):
    def test_auc_and_ties(self):
        self.assertEqual(ranking_metrics([2,1],[True,False])['auc'],1)
        self.assertEqual(ranking_metrics([1,2],[True,False])['auc'],0)
        self.assertEqual(ranking_metrics([0,0],[True,False])['auc'],.5)
        m=ranking_metrics([0]*20,[True]*4+[False]*16)
        self.assertEqual(m['top_five_percent']['expected_hits'],.2)
        self.assertEqual(m['top_five_percent']['min_hits'],0)
        self.assertEqual(m['top_five_percent']['max_hits'],1)
        self.assertEqual(ranking_metrics([1,0],[True,False])['ndcg']['5']['value'],1)
        self.assertIsNone(ranking_metrics([],[])['auc'])
        self.assertIsNone(ranking_metrics([1],[False])['ndcg']['5']['value'])
        self.assertEqual(ranking_metrics([1],[True])['ndcg']['5']['value'],1)
        for v in [True,float('nan'),float('inf')]:
            with self.assertRaises(ValueError):ranking_metrics([v],[True])

    def fixture(self):
        path=os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not path:self.skipTest('Go synthetic fixture not supplied')
        return json.loads(Path(path).read_bytes())

    def test_report_denominator_and_determinism(self):
        d=self.fixture();a=evaluate(d,'a'*64,synthetic=True);b=evaluate(d,'a'*64,synthetic=True)
        self.assertEqual(a,b);self.assertEqual(a['queries'][0]['counts']['N'],25)
        self.assertEqual(a['queries'][0]['counts']['eligible'],25)
        self.assertEqual(a['queries'][0]['methods']['mlp']['status'],'pending')
        self.assertEqual(a['all_selected_positive_declared_episode_keys'],15)
        d['ready']=False;d['blockers']=['source_missing'];d['rows'][70]['label']=None;d['rows'][70]['eligible']=False;d['rows'][70]['blockers']=['unknown_label']
        r=evaluate(d,'a'*64,synthetic=True)
        self.assertEqual(r['status'],'waiting_for_data');self.assertEqual(r['queries'][0]['counts']['label_unknown'],1)
        self.assertIsNone(r['queries'][0]['methods']['uniform']['metrics'])

    def test_invalid_and_synthetic_boundaries(self):
        d=self.fixture()
        with self.assertRaises(ValueError):evaluate(d,'a'*64)
        for mutate in [lambda x:x['rows'].append(x['rows'][0]),lambda x:x['rows'][0].update(generation=-1),lambda x:x['rows'][0].update(split='test'),lambda x:x['rows'][0]['features']['numeric'].update(episode_count=True),lambda x:x['spec'].update(train_end=x['spec']['anchor']),lambda x:x['schema'][0].update(name='extra'),lambda x:x['rows'][0].update(label=None),lambda x:x['rows'][0].update(entity_ref=''),lambda x:x['rows'][0].update(eligible=False),lambda x:x.update(implementation_choices='bad'),lambda x:x.update(counts={'N':True})]:
            c=copy.deepcopy(d);c['ready']=False;mutate(c)
            with self.assertRaises((ValueError,KeyError)):evaluate(c,'a'*64,synthetic=True)
        with tempfile.TemporaryDirectory() as root:
            p=Path(root)/'x.json';p.write_text('{"x":1,"x":2}')
            with self.assertRaises(ValueError):read_file(p,hashlib.sha256(p.read_bytes()).hexdigest())
            with self.assertRaises(ValueError):read_file(p,'0'*64)

    def test_existing_checkpoint_no_fit(self):
        from train import train
        d=self.fixture();a,_,_,_=train(d,'a'*64,{'seed':17,'epochs':1},True)
        before=json.dumps(a,sort_keys=True)
        r=evaluate(d,'a'*64,artifact=a,artifact_sha='b'*64,synthetic=True)
        self.assertEqual(r['queries'][0]['methods']['mlp']['status'],'computed')
        self.assertEqual(before,json.dumps(a,sort_keys=True))
        a['dataset_sha']='c'*64
        with self.assertRaises(ValueError):evaluate(d,'a'*64,artifact=a,artifact_sha='b'*64,synthetic=True)

    def test_cli_blocked_checkpoint_is_not_opened(self):
        d=self.fixture();d['ready']=False;d['blockers']=['source_missing']
        with tempfile.TemporaryDirectory() as root:
            p=Path(root)/'dataset.json';p.write_text(json.dumps(d));sha=hashlib.sha256(p.read_bytes()).hexdigest()
            base=[sys.executable,str(Path(__file__).with_name('evaluate.py')),'self-test','--dataset',str(p),'--dataset-sha',sha,'--artifact-file',str(Path(root)/'missing-model.json'),'--artifact-sha','b'*64]
            cp=subprocess.run(base+['--output',str(Path(root)/'waiting')],capture_output=True)
            self.assertEqual(cp.returncode,0,cp.stderr.decode())
            report=json.loads(Path(root,'waiting','report.json').read_text())
            self.assertEqual(report['status'],'waiting_for_data')
            self.assertFalse(report['input']['artifact_loaded_and_scored'])
            self.assertIsNone(report['input']['verified_loaded_artifact_sha'])
            self.assertTrue(all(v['metrics'] is None for v in report['queries'][0]['methods'].values()))
            d['ready']=True;d['blockers']=[];p.write_text(json.dumps(d));base[base.index('--dataset-sha')+1]=hashlib.sha256(p.read_bytes()).hexdigest()
            cp=subprocess.run(base+['--output',str(Path(root)/'rejected')],capture_output=True)
            self.assertNotEqual(cp.returncode,0)

    def test_episode_scope_and_history_unknown(self):
        d=self.fixture();d['ready']=False;d['blockers']=['insufficient_source']
        positive=d['rows'][70];positive['eligible']=False;positive['blockers']=['entity_partition']
        d['rows'][71]['episode_keys']=positive['episode_keys'][:]
        duplicate=copy.deepcopy(d['rows'][71]);duplicate['query_id']=99;d['rows'].append(duplicate)
        zero=d['rows'][72];zero['features']['numeric']['episode_count']=0;zero['features']['history_unknown']=False
        unknown=d['rows'][73];unknown['features']['numeric']['episode_count']=0;unknown['features']['history_unknown']=True
        r=evaluate(d,'a'*64,synthetic=True)
        self.assertEqual(r['all_selected_positive_declared_episode_keys'],12)
        self.assertEqual(r['common_cohort_positive_declared_episode_keys'],9)
        q=next(x for x in r['queries'] if x['query_id']==2)
        self.assertEqual(q['counts']['N'],25)
        self.assertEqual(q['counts']['history_unknown'],1)
        self.assertEqual(q['counts']['method_no_signal'],1)
        self.assertEqual(q['counts']['eligible'],23)
        self.assertEqual(next(x for x in r['queries'] if x['query_id']==99)['counts']['N'],1)

    def test_checkpoint_test_selection_declaration(self):
        from train import train
        d=self.fixture();artifact,_,_,_=train(d,'a'*64,{'seed':17,'epochs':1},True)
        for split in ('validation','test'):
            self.assertEqual(evaluate(d,'a'*64,split,artifact,'b'*64,True)['queries'][0]['methods']['mlp']['status'],'computed')
            for value in (True,0,None,'false'):
                changed=copy.deepcopy(artifact);changed['selection']['test_used_for_selection']=value
                with self.assertRaises(ValueError):evaluate(d,'a'*64,split,changed,'b'*64,True)
            changed=copy.deepcopy(artifact);del changed['selection']['test_used_for_selection']
            with self.assertRaises(ValueError):evaluate(d,'a'*64,split,changed,'b'*64,True)
            changed=copy.deepcopy(artifact);del changed['selection']
            with self.assertRaises(ValueError):evaluate(d,'a'*64,split,changed,'b'*64,True)
