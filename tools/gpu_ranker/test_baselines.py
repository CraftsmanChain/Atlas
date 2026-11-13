import copy
import json
import os
import unittest
from datetime import timedelta
from pathlib import Path
from baselines import at, health_inputs, weight_inputs
from evaluate import evaluate


class BaselineTests(unittest.TestCase):
    def fixture(self):
        path = os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not path:
            self.skipTest('Go fixture not supplied')
        return json.loads(Path(path).read_text())

    def binding(self, d, version):
        return dict(version=version, dataset_sha='a'*64, target=d['target'], profile=d['spec']['profile'], feature_contract=d['feature_contract'])

    def health(self, d):
        h = self.binding(d, 'gpu-ranking-health-sidecar-v1'); h['rows'] = []
        for r in d['rows']:
            q = at(r['cutoff']); old = (q-timedelta(hours=1)).isoformat()
            h['rows'].append(dict(query_id=r['query_id'], node_id=r['node_id'], generation=r['generation'], cutoff=r['cutoff'], scope_sha='b'*64, result_sha='c'*64, node_available_at=old, identity_changed_at=old, expected=1, observed=1, cards=[dict(asset_id=r['node_id'], uuid=str(r['node_id']), first_seen_at=old, state='active', health_id=1, run_id=1, rule='synthetic', score=80, evaluated_at=old, available_at=old)]))
        return h

    def weights(self, d):
        w = self.binding(d, 'gpu-ranking-severity-weights-v1')
        w.update(dimension='severity', formula='sum(count*weight)', selection_policy='predeclared_no_outcome_selection', test_used_for_selection=False, frozen_at=d['spec']['anchor'], weights={'synthetic': 2})
        return w

    def test_health_time_and_identity(self):
        d = self.fixture(); h = self.health(d)
        self.assertEqual(next(iter(health_inputs(h,d,'a'*64).values()))['score'],20)
        for field in ['evaluated_at','available_at','first_seen_at']:
            for offset in [0,1]:
                bad=copy.deepcopy(h); bad['rows'][0]['cards'][0][field]=(at(bad['rows'][0]['cutoff'])+timedelta(seconds=offset)).isoformat()
                with self.assertRaises(ValueError): health_inputs(bad,d,'a'*64)
        row=h['rows'][0]; card=row['cards'][0]
        card['first_seen_at']=(at(row['cutoff'])-timedelta(days=2)).isoformat(); row['identity_changed_at']=card['first_seen_at']
        card['evaluated_at']=(at(row['cutoff'])-timedelta(hours=24)).isoformat()
        self.assertEqual(health_inputs(h,d,'a'*64)[(row['query_id'],row['node_id'],row['generation'])]['score'],20)
        card['available_at']=(at(card['evaluated_at'])-timedelta(seconds=1)).isoformat()
        health_inputs(h,d,'a'*64)  # R1 does not require evaluated <= created/run availability.
        card['evaluated_at']=(at(card['evaluated_at'])-timedelta(seconds=1)).isoformat()
        with self.assertRaises(ValueError):health_inputs(h,d,'a'*64)
        for change in [lambda x:x['rows'].append(x['rows'][0]),lambda x:x['rows'][0].update(generation=999),lambda x:x.update(dataset_sha='f'*64),lambda x:x.update(profile='unknown')]:
            bad=self.health(d);change(bad)
            with self.assertRaises(ValueError):health_inputs(bad,d,'a'*64)
        bad=self.health(d);a,b=bad['rows'][:2];b['cards'][0]['uuid']=a['cards'][0]['uuid']
        with self.assertRaises(ValueError):health_inputs(bad,d,'a'*64)

    def test_joint_keeps_base_and_missing(self):
        d=self.fixture();h=self.health(d);selected=[r for r in h['rows'] if next(x for x in d['rows'] if x['query_id']==r['query_id'])['split']=='validation']
        selected[0]['cards'][0]['score']=None;selected[1]['expected']=2
        a=evaluate(d,'a'*64,synthetic=True,health=h,health_sha='b'*64)
        b=evaluate(d,'a'*64,synthetic=True,health=h,health_sha='b'*64)
        self.assertEqual(a,b);q=a['queries'][0];joint=q['baseline_comparison']
        self.assertEqual(q['counts']['eligible'],25);self.assertEqual(joint['joint_eligible'],23)
        self.assertEqual(joint['diagnostics']['health_inverse']['states'],{'complete':23,'no_signal':1,'partial':1})
        self.assertEqual({x['cohort_sha'] for x in joint['methods'].values()},{joint['cohort_sha']})
        d['ready']=False
        blocked=evaluate(d,'a'*64,synthetic=True,health_sha='b'*64)
        self.assertEqual(blocked['queries'][0]['baseline_comparison']['joint_eligible'],0)
        self.assertIsNone(blocked['input']['verified_loaded_health_inverse_sha'])

    def test_weights_unknown_zero_and_policy(self):
        d=self.fixture();w=self.weights(d);r=d['rows'][0]
        r['features']['numeric']['episode_count']=2;r['features']['by_severity']={'synthetic':2}
        k=(r['query_id'],r['node_id'],r['generation'])
        self.assertEqual(weight_inputs(w,d,'a'*64,[])[k]['score'],4)
        r['features']['by_severity']={'other':2};self.assertIsNone(weight_inputs(w,d,'a'*64,[])[k]['score'])
        r['features']['numeric']['episode_count']=0;r['features']['by_severity']={};self.assertEqual(weight_inputs(w,d,'a'*64,[])[k]['score'],0)
        r['features']['by_severity']=None;self.assertIsNone(weight_inputs(w,d,'a'*64,[])[k]['score'])
        for v in [True,0,None,'false']:
            bad=copy.deepcopy(w);bad['test_used_for_selection']=v
            with self.assertRaises(ValueError):weight_inputs(bad,d,'a'*64,[])
        for v in [-1,True,float('inf')]:
            bad=copy.deepcopy(w);bad['weights']['synthetic']=v
            with self.assertRaises(ValueError):weight_inputs(bad,d,'a'*64,[])
        bad=copy.deepcopy(w);bad['frozen_at']=d['spec']['test_end']
        with self.assertRaises(ValueError):weight_inputs(bad,d,'a'*64,[])

    def test_closed_schema_and_numeric_budget(self):
        d=self.fixture();h=self.health(d)
        for field in ['node_available_at','identity_changed_at']:
            bad=copy.deepcopy(h);bad['rows'][0][field]=None
            with self.assertRaises(ValueError):health_inputs(bad,d,'a'*64)
        for mutate in [lambda x:x.update(extra=1),lambda x:x['rows'][0].update(extra=1),lambda x:x['rows'][0]['cards'][0].update(extra=1)]:
            bad=copy.deepcopy(h);mutate(bad)
            with self.assertRaises(ValueError):health_inputs(bad,d,'a'*64)
        w=self.weights(d);w['weights']['synthetic']=10**400
        with self.assertRaises(ValueError):weight_inputs(w,d,'a'*64,[])

    def test_cli_pinning_and_blocked_no_read(self):
        import hashlib
        import subprocess
        import sys
        import tempfile
        with tempfile.TemporaryDirectory() as td:
            p=Path(td);d=self.fixture();h=self.health(d)
            data=p/'dataset.json';data.write_text(json.dumps(d));ds=hashlib.sha256(data.read_bytes()).hexdigest();h['dataset_sha']=ds
            health=p/'health.json';health.write_text(json.dumps(h));hs=hashlib.sha256(health.read_bytes()).hexdigest()
            base=[sys.executable,str(Path(__file__).with_name('evaluate.py')),'self-test','--dataset',str(data),'--dataset-sha',ds,'--health-file',str(health)]
            a=subprocess.run(base+['--health-sha',hs,'--output',str(p/'out1')],capture_output=True)
            self.assertEqual(a.returncode,0,a.stderr)
            b=subprocess.run(base+['--health-sha',hs,'--output',str(p/'out2')],capture_output=True)
            self.assertEqual(b.returncode,0,b.stderr)
            self.assertEqual((p/'out1/report.json').read_bytes(),(p/'out2/report.json').read_bytes())
            bad=subprocess.run(base+['--health-sha','f'*64,'--output',str(p/'out3')],capture_output=True)
            self.assertEqual(bad.returncode,2)
            health.write_text('{"version":1,"version":2}')
            hs=hashlib.sha256(health.read_bytes()).hexdigest()
            bad=subprocess.run(base+['--health-sha',hs,'--output',str(p/'out4')],capture_output=True)
            self.assertEqual(bad.returncode,2);self.assertNotIn(b'Traceback',bad.stderr)
            d['ready']=False;data.write_text(json.dumps(d));ds=hashlib.sha256(data.read_bytes()).hexdigest();health.unlink()
            blocked=subprocess.run([sys.executable,str(Path(__file__).with_name('evaluate.py')),'self-test','--dataset',str(data),'--dataset-sha',ds,'--health-file',str(health),'--health-sha','e'*64,'--output',str(p/'blocked')],capture_output=True)
            self.assertEqual(blocked.returncode,0,blocked.stderr)
