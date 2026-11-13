import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import time

SCRIPT = Path(__file__).with_name('deploy_remote_source.sh').resolve()
REAL_GIT = shutil.which('git')


class PushTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / 'source'
        self.repo.mkdir()
        self.run_git('init', '-b', 'main')
        self.run_git('config', 'user.name', 'fixture')
        self.run_git('config', 'user.email', 'fixture@example.invalid')
        (self.repo / 'source.txt').write_text('committed source')
        self.run_git('add', '.')
        self.run_git('commit', '-m', 'fixture')
        self.targets = [self.root / 'github.git', self.root / 'gitlab.git']
        for target in self.targets:
            subprocess.run([REAL_GIT, 'init', '--bare', str(target)], check=True, capture_output=True)
        self.env = dict(os.environ, GITHUB_PUSH_TARGET=str(self.targets[0]),
                        GITLAB_PUSH_TARGET=str(self.targets[1]), ATLAS_DEPLOY_TARGET='must-not-load',
                        ATLAS_DEPLOYMENT_STAGE='invalid-for-deploy', GIT_TERMINAL_PROMPT='0')
        for key in ['BRANCH', 'EXPECTED_COMMIT', 'SKIP_GIT_PUSH']:
            self.env.pop(key, None)

    def run_git(self, *args):
        return subprocess.check_output([REAL_GIT, *args], cwd=self.repo, stderr=subprocess.DEVNULL, text=True).strip()

    def invoke(self):
        return subprocess.run(['bash', str(SCRIPT), '--push-only'], cwd=self.repo, env=self.env,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=12)

    def test_real_two_bare_repositories_and_no_deployment(self):
        sentinels = self.root / 'sentinels'
        sentinels.mkdir()
        for tool in ['ssh', 'npm', 'rsync', 'curl', 'jq']:
            path = sentinels / tool
            path.write_text('#!/bin/sh\nexit 99\n')
            path.chmod(0o755)
        self.env['PATH'] = str(sentinels) + ':' + self.env['PATH']
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout)
        for target in self.targets:
            actual = subprocess.check_output([REAL_GIT, '--git-dir', str(target), 'rev-parse', 'main'], text=True).strip()
            self.assertEqual(actual, self.run_git('rev-parse', 'HEAD'))

    def test_failed_first_still_pushes_second(self):
        self.env['GITHUB_PUSH_TARGET'] = str(self.root / 'missing.git')
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('[GitHub] FAILED', result.stdout)
        self.assertIn('[GitLab] SUCCESS', result.stdout)
        self.assertEqual(subprocess.check_output([REAL_GIT, '--git-dir', str(self.targets[1]), 'rev-parse', 'main'], text=True).strip(), self.run_git('rev-parse', 'HEAD'))

    def mock_git(self, behavior):
        tools = self.root / 'mock-tools'; tools.mkdir()
        path = tools / 'git'
        path.write_text('#!/usr/bin/env python3\nimport os,sys,time,subprocess\nfrom pathlib import Path\nif sys.argv[1]!="push":os.execv(os.environ["REAL_GIT"],[os.environ["REAL_GIT"],*sys.argv[1:]])\n' + behavior)
        path.chmod(0o755)
        self.env.update(PATH=str(tools) + ':' + self.env['PATH'], REAL_GIT=REAL_GIT, MARKERS=str(self.root))

    def test_pushes_begin_concurrently(self):
        self.mock_git('''assert sys.argv[4].split(':')[0]==os.environ['PINNED_SHA']
label=Path(sys.argv[3]).name
Path(os.environ['MARKERS'],label+'.started').touch()
other='gitlab.git' if label=='github.git' else 'github.git'
for i in range(100):
 if Path(os.environ['MARKERS'],other+'.started').exists():sys.exit(0)
 time.sleep(.02)
sys.exit(93)
''')
        self.env['PINNED_SHA'] = self.run_git('rev-parse', 'HEAD')
        result = self.invoke(); self.assertEqual(result.returncode, 0, result.stdout)

    def test_timeout_does_not_prevent_other_destination(self):
        self.mock_git("if 'github' in sys.argv[3]:time.sleep(20)\nsys.exit(0)\n")
        self.env['GIT_PUSH_TIMEOUT_SECONDS'] = '.2'
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('[GitHub] FAILED exit=124', result.stdout)
        self.assertIn('[GitLab] SUCCESS', result.stdout)

    def test_timeout_stops_transport_children(self):
        self.mock_git("if 'github' in sys.argv[3]:\n subprocess.Popen([sys.executable,'-c',\"import time,os;from pathlib import Path;Path(os.environ['MARKERS'],'child.started').touch();time.sleep(2);Path(os.environ['MARKERS'],'child.leaked').touch()\"])\n time.sleep(20)\nsys.exit(0)\n")
        self.env['GIT_PUSH_TIMEOUT_SECONDS'] = '.8'
        result = self.invoke()
        self.assertIn('[GitHub] FAILED exit=124', result.stdout)
        self.assertTrue((self.root / 'child.started').exists())
        time.sleep(2.1)
        self.assertFalse((self.root / 'child.leaked').exists())

    def test_source_guard_and_destination_branch(self):
        self.env['EXPECTED_COMMIT'] = '0000000'
        self.assertNotEqual(self.invoke().returncode, 0)
        self.env.pop('EXPECTED_COMMIT')
        self.env['BRANCH'] = 'release/source'
        self.assertEqual(self.invoke().returncode, 0)
        self.env['BRANCH'] = '../invalid'
        self.assertNotEqual(self.invoke().returncode, 0)
        (self.repo / 'dirty').touch()
        self.env.pop('BRANCH')
        self.assertIn('Working tree must be clean', self.invoke().stdout)

    def test_non_fast_forward_does_not_force(self):
        self.assertEqual(self.invoke().returncode, 0)
        other = self.root / 'other'
        subprocess.run([REAL_GIT, 'clone', str(self.targets[0]), str(other)], check=True, capture_output=True)
        subprocess.run([REAL_GIT, '-C', str(other), 'checkout', 'main'], check=True, capture_output=True)
        for k,v in [('user.name','fixture'),('user.email','fixture@example.invalid')]:
            subprocess.run([REAL_GIT,'-C',str(other),'config',k,v],check=True)
        (other/'later').touch()
        for args in [('add','.'),('commit','-m','ahead'),('push','origin','main')]:
            subprocess.run([REAL_GIT,'-C',str(other),*args],check=True,capture_output=True)
        result=self.invoke();self.assertNotEqual(result.returncode,0)
        self.assertIn('[GitHub] FAILED',result.stdout);self.assertIn('[GitLab] SUCCESS',result.stdout)


if __name__ == '__main__':
    unittest.main()
