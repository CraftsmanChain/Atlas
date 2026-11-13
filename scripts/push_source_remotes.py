#!/usr/bin/env python3
"""Push the current committed source to independent destinations, without deployment."""
import concurrent.futures
import os
import re
import signal
import subprocess
import sys


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def push(label, target, branch, commit, timeout):
    env = os.environ.copy()
    env['GIT_TERMINAL_PROMPT'] = '0'
    env.setdefault('GIT_SSH_COMMAND', 'ssh -o BatchMode=yes -o ConnectTimeout=10')
    try:
        process = subprocess.Popen(['git', 'push', '--', target, commit + ':refs/heads/' + branch],
                                   env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                   start_new_session=True)
        try:
            output, _ = process.communicate(timeout=timeout)
            code = process.returncode
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                output, _ = process.communicate(timeout=2)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
                try:
                    output, _ = process.communicate(timeout=2)
                except subprocess.TimeoutExpired as error:
                    output = error.output or b''
                    process.stdout.close()
                    process.wait(timeout=2)
            code = 124
        return label, code, output.decode(errors='replace')
    except subprocess.TimeoutExpired as error:
        # Cleanup must not escape the worker and suppress this destination's summary.
        output = error.output or locals().get('output', b'')
        if isinstance(output, bytes):
            output = output.decode(errors='replace')
        return label, 124, output
    except (OSError, ValueError) as error:
        return label, 127, type(error).__name__


def main():
    try:
        os.chdir(git('rev-parse', '--show-toplevel'))
        if git('status', '--porcelain'):
            raise ValueError('Working tree must be clean before pushing.')
        commit = git('rev-parse', 'HEAD')
        expected = os.environ.get('EXPECTED_COMMIT', '')
        if expected and not re.fullmatch('[0-9a-f]{7,40}', expected):
            raise ValueError('EXPECTED_COMMIT must be a 7-40 character lowercase commit SHA.')
        if expected and not commit.startswith(expected):
            raise ValueError('Source commit mismatch; no push performed.')
        branch = os.environ.get('BRANCH') or git('symbolic-ref', '--quiet', '--short', 'HEAD')
        if subprocess.call(['git', 'check-ref-format', 'refs/heads/' + branch],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL):
            raise ValueError('BRANCH must be a valid destination branch.')
        timeout = float(os.environ.get('GIT_PUSH_TIMEOUT_SECONDS', '60'))
        if not 0 < timeout <= 3600:
            raise ValueError('GIT_PUSH_TIMEOUT_SECONDS must be between 0 and 3600.')
        targets = [('GitHub', os.environ.get('GITHUB_PUSH_TARGET') or
                    os.environ.get('EXTRA_GIT_REMOTE') or 'git@github.com:CraftsmanChain/Atlas.git'),
                   ('GitLab', os.environ.get('GITLAB_PUSH_TARGET') or
                    os.environ.get('GIT_REMOTE') or 'http://10.255.151.17:9091/sre/atlas.git')]
        print('Pushing committed source ' + commit + ' to branch ' + branch, flush=True)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(push, label, target, branch, commit, timeout) for label, target in targets]
            results = []
            for future in concurrent.futures.as_completed(futures):
                label, code, output = future.result()
                results.append((label, code, output))
                print('\n[' + label + '] ' + ('SUCCESS' if code == 0 else 'FAILED') + ' exit=' + str(code), flush=True)
                print(output.rstrip(), flush=True)
        print('Push summary: ' + ', '.join(label + '=' + str(code) for label, code, _ in results), flush=True)
        return int(any(code != 0 for _, code, _ in results))
    except (ValueError, subprocess.CalledProcessError) as error:
        print(str(error) if isinstance(error, ValueError) else 'Git source preflight failed.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
