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
from train_ablation import train_candidate, reload_scores
from train import train

class AblationTrainingTests(unittest.TestCase):
    def fixture(self):
        p=os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not p:self.skipTest('Go fixture required')
        return json.loads(Path(p).read_bytes())

    def test_three_objectives_reload_and_determinism(self):
        d=self.fixture()
        for m in ('core_all','drop_composition','drop_recurrence','drop_calendar','drop_host_context','drop_host_id'):
            a,z=train_candidate(d,'a'*64,m,epochs=1,synthetic=True)
            b,w=train_candidate(d,'a'*64,m,epochs=1,synthetic=True)
            self.assertEqual(a,b);self.assertEqual(z,w);self.assertEqual(z,reload_scores(a,d,'a'*64))
            self.assertIs(a['selection']['test_used_for_selection'],False)
            bad=copy.deepcopy(a);bad['layers'][0]['weights'][0][0]=float('nan')
            with self.assertRaises(ValueError):reload_scores(bad,d,'a'*64)
            bad=copy.deepcopy(a);bad['selection']['test_used_for_selection']=0
            with self.assertRaises(ValueError):reload_scores(bad,d,'a'*64)

    def test_pointwise_compatibility_and_train_fit_only(self):
        import feature_ablation
        d=self.fixture();seen=[];actual=feature_ablation.fit
        with patch('feature_ablation.fit',side_effect=lambda rows:(seen.extend(r['split'] for r in rows),actual(rows))[1]):a,z=train_candidate(d,'a'*64,'core_all',epochs=1,synthetic=True)
        old,oldz,_,_=train(d,'a'*64,{'seed':17,'epochs':1},True)
        self.assertEqual(set(seen),{'train'});self.assertEqual(a['layers'],old['layers']);self.assertEqual(z,oldz)

    def test_test_labels_do_not_select(self):
        d=self.fixture();other=copy.deepcopy(d);rows=[r for r in other['rows'] if r['split']=='test'];values=[(r['label'],r['episode_keys']) for r in rows]
        for r,(label,keys) in zip(rows,values[5:]+values[:5]):r['label']=label;r['episode_keys']=keys
        for m in ('core_all','drop_composition','drop_recurrence','drop_calendar','drop_host_context','drop_host_id'):
            a,_=train_candidate(d,'a'*64,m,epochs=1,synthetic=True);b,_=train_candidate(other,'b'*64,m,epochs=1,synthetic=True)
            for k in ('layers','selection'):self.assertEqual(a[k],b[k])
            self.assertEqual(a['wrapper']['preprocessor'],b['wrapper']['preprocessor'])

    def test_closed_reload_metadata(self):
        d=self.fixture();a,_=train_candidate(d,'a'*64,'core_all',epochs=1,synthetic=True)
        mutations=[lambda x:x.update(extra=True),lambda x:x.update(objective='listnet'),lambda x:x['config'].update(epochs=0),lambda x:x['config'].update(device='cuda'),lambda x:x['config'].update(extra=1),lambda x:x['selection'].update(best_epoch=2),lambda x:x['selection'].update(validation_loss=float('nan')),lambda x:x['diagnostics']['train'].update(rows=1),lambda x:x['source_sha'].update(extra='a'*64)]
        for mutate in mutations:
            b=copy.deepcopy(a);mutate(b)
            with self.assertRaises(ValueError):reload_scores(b,d,'a'*64)

    def test_gates_before_fit(self):
        d=self.fixture();d['ready']=False
        with patch('feature_ablation.fit',side_effect=AssertionError('must not fit')):
            with self.assertRaises(ValueError):train_candidate(d,'a'*64,'drop_composition',epochs=1,synthetic=True)
        with self.assertRaises(ValueError):train_candidate(self.fixture(),'a'*64,'core_all',epochs=1)

    def test_cli_files_score_and_wrong_pin(self):
        d=self.fixture()
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);p=root/'d.json';p.write_text(json.dumps(d));sha=hashlib.sha256(p.read_bytes()).hexdigest();tool=str(Path(__file__).with_name('train_ablation.py'))
            args=[sys.executable,tool,'self-test','--dataset',str(p),'--dataset-sha',sha,'--output',str(root/'out'),'--policy','drop_composition','--epochs','1']
            cp=subprocess.run(args,capture_output=True);self.assertEqual(cp.returncode,0,cp.stderr)
            model=root/'out/model.json';h=hashlib.sha256(model.read_bytes()).hexdigest();manifest=json.loads((root/'out/manifest.json').read_text());self.assertEqual(manifest['files']['model.json'],h)
            score=[sys.executable,tool,'score','--dataset',str(p),'--dataset-sha',sha,'--artifact-file',str(model),'--artifact-sha',h,'--output',str(root/'score')]
            cp=subprocess.run(score,capture_output=True);self.assertEqual(cp.returncode,0,cp.stderr)
            self.assertEqual((root/'score/raw-scores.json').read_bytes(),(root/'out/raw-scores.json').read_bytes())
            score[score.index('--artifact-sha')+1]='0'*64;score[score.index('--output')+1]=str(root/'bad');cp=subprocess.run(score,capture_output=True);self.assertEqual(cp.returncode,2)

    def test_available_profile_and_cross_binding(self):
        p=os.environ.get('ATLAS_AVAILABLE_CONTEXT_FIXTURE')
        if not p:self.skipTest('Available fixture required')
        d=json.loads(Path(p).read_bytes())
        for policy in ('core_all','drop_host_context'):
            a,z=train_candidate(d,'b'*64,policy,epochs=1,synthetic=True)
            self.assertEqual(z,reload_scores(a,d,'b'*64))
            with self.assertRaises(ValueError):reload_scores(a,self.fixture(),'a'*64)
