#!/usr/bin/env python3
"""The required status must reject any failed, skipped or cancelled validation."""

import json
import os
from pathlib import Path
import re
import subprocess
import textwrap
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / '.github/workflows/ci.yml'


def job_blocks(raw):
    jobs = raw.split('\njobs:\n', 1)[1]
    starts = list(re.finditer(r'(?m)^  ([A-Za-z0-9_-]+):\s*$', jobs))
    return {match[1]: jobs[match.end():starts[index + 1].start() if index + 1 < len(starts) else len(jobs)]
            for index, match in enumerate(starts)}


class RequiredStatusTest(unittest.TestCase):
    def setUp(self):
        self.jobs = job_blocks(WORKFLOW.read_text())
        self.required = ['build-checks', 'api-coverage', 'hosted-workers', 'postgres',
                         'docker-lifecycle', 'frontend', 'security']

    def test_required_status_covers_every_validation_branch(self):
        gate = self.jobs['test']
        needs = re.search(r'(?m)^    needs: \[([^\]]+)\]$', gate)
        self.assertIsNotNone(needs)
        self.assertEqual([job.strip() for job in needs[1].split(',')], self.required)
        self.assertRegex(gate, r'(?m)^    if: always\(\)$')
        self.assertRegex(self.jobs['api-coverage'], r'(?m)^    needs: \[api-race, go-race\]$')
        self.assertIn('--verify api-race-results --count 8', self.jobs['api-coverage'])
        self.assertIn('--verify go-race-results --count 6', self.jobs['api-coverage'])

    def test_coverage_rejects_failed_matrices_even_with_complete_artifacts(self):
        body = self.jobs['api-coverage'].split('        run: |\n', 1)[1].split('      - uses:', 1)[0]
        command = textwrap.dedent(body)
        for result in ('success', 'failure', 'cancelled', 'skipped'):
            env = dict(os.environ, RACE_RESULTS=json.dumps({'api-race': {'result': result},
                                                          'go-race': {'result': 'success'}}))
            process = subprocess.run(['bash', '-e', '-c', command], env=env, capture_output=True)
            self.assertEqual(process.returncode == 0, result == 'success', process.stderr)

    def test_race_suites_run_in_parallel_with_bounded_jobs(self):
        for name in ('api-race', 'go-race', 'frontend', 'security'):
            self.assertNotRegex(self.jobs[name], r'(?m)^    needs:')
        for name, body in self.jobs.items():
            match = re.search(r'(?m)^    timeout-minutes: (\d+)$', body)
            self.assertIsNotNone(match, name)
            self.assertLessEqual(int(match[1]), 10, name)
        self.assertNotIn('--other-packages', self.jobs['build-checks'])

    def test_gate_rejects_failed_cancelled_and_skipped_results_for_each_job(self):
        body = self.jobs['test'].split('        run: |\n', 1)[1]
        command = textwrap.dedent(body)
        good = {name: {'result': 'success'} for name in self.required}
        for name in self.required:
            for result in ('success', 'failure', 'cancelled', 'skipped'):
                with self.subTest(job=name, result=result):
                    results = dict(good, **{name: {'result': result}})
                    env = dict(os.environ, JOB_RESULTS=json.dumps(results))
                    process = subprocess.run(['bash', '-e', '-c', command], env=env, capture_output=True)
                    self.assertEqual(process.returncode == 0, result == 'success', process.stderr)


if __name__ == '__main__':
    unittest.main()
