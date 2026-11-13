import copy
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from period_report import build, stratum

class PeriodTests(unittest.TestCase):
    def fixture(self):
        p=os.environ.get('ATLAS_GPU_RISK_MLP_FIXTURE')
        if not p:self.skipTest('Go fixture required')
        return json.loads(Path(p).read_bytes())

    def report(self,d,start='2026-03-02T00:00:00Z',end='2026-03-03T00:00:00Z'):
        return build(d,'a'*64,start,end,synthetic=True)

    def test_boundaries_and_strata(self):
        d=self.fixture();r=self.report(d)
        self.assertEqual(r['counts']['N'],25)
        self.assertEqual(sum(x['N'] for x in r['summary'].values()),25)
        self.assertEqual(r['summary']['zero_observed_failures']['N'],3)
        self.assertEqual(r,self.report(d,'2026-03-02T08:00:00+08:00','2026-03-03T08:00:00+08:00'))
        self.assertEqual(self.report(d,'2026-03-01T00:00:00Z','2026-03-02T00:00:00Z')['counts']['N'],0)
        for a,b in [('2026-03-02','2026-03-03'),('2026-03-03T00:00:00Z','2026-03-02T00:00:00Z')]:
            with self.assertRaises(ValueError):self.report(d,a,b)
        row=copy.deepcopy(d['rows'][0]);row['features']['numeric']['episode_count']=0
        self.assertEqual(stratum(row),'zero_observed_failures')
        row['features']['history_unknown']=True;self.assertEqual(stratum(row),'history_unknown')
        row['features']['history_unknown']=False;row['features']['numeric']['episode_count']=None
        self.assertEqual(stratum(row),'history_unknown')

    def test_blocked_and_dedup(self):
        d=self.fixture();d['ready']=False;d['blockers']=['missing_source']
        first=next(r for r in d['rows'] if r['split']=='validation')
        duplicate=copy.deepcopy(first);duplicate['query_id']=99;d['rows'].append(duplicate)
        first['features']['history_unknown']=True
        r=self.report(d)
        self.assertEqual(r['counts']['N'],26);self.assertEqual(r['counts']['unique_node_generations'],25)
        self.assertEqual(r['counts']['positive_declared_episode_keys'],15)
        self.assertEqual(r['summary']['history_unknown']['N'],1)
        for q in r['queries']:
            for s in q['strata'].values():
                for m in s['methods'].values():self.assertIsNone(m['metrics'])
        self.assertIsNone(r['summary']['observed_history']['methods']['uniform']['auc_mean'])

    def test_null_auc_and_label_independent_strata(self):
        d=self.fixture();r=self.report(d)
        self.assertIsNone(r['queries'][0]['strata']['zero_observed_failures']['methods']['uniform']['metrics']['auc'])
        row=d['rows'][0];name=stratum(row);row['label']=not row['label'];self.assertEqual(name,stratum(row))
        with self.assertRaises(ValueError):build(d,'a'*64,'2026-03-02T00:00:00Z','2026-03-03T00:00:00Z')

    def test_cli_determinism_and_rejection(self):
        d=self.fixture()
        with tempfile.TemporaryDirectory() as td:
            p=Path(td);file=p/'dataset.json';file.write_text(json.dumps(d));sha=hashlib.sha256(file.read_bytes()).hexdigest()
            args=[sys.executable,str(Path(__file__).with_name('period_report.py')),'self-test','--dataset',str(file),'--dataset-sha',sha,'--period-start','2026-03-02T00:00:00Z','--period-end','2026-03-03T00:00:00Z']
            for name in ['a','b']:
                cp=subprocess.run(args+['--output',str(p/name)],capture_output=True);self.assertEqual(cp.returncode,0,cp.stderr)
            self.assertEqual((p/'a/report.json').read_bytes(),(p/'b/report.json').read_bytes())
            for payload in ['{}','{"a":1,"a":2}']:
                file.write_text(payload);bad=args.copy();bad[bad.index('--dataset-sha')+1]=hashlib.sha256(file.read_bytes()).hexdigest()
                cp=subprocess.run(bad+['--output',str(p/'bad')],capture_output=True);self.assertEqual(cp.returncode,2);self.assertNotIn(b'Traceback',cp.stderr)
            cp=subprocess.run(args+['--output',str(p/'sha')],capture_output=True);self.assertEqual(cp.returncode,2)
