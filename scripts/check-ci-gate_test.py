#!/usr/bin/env python3
"""Keep the existing required status green only after all former checks pass."""

import os
from pathlib import Path
import re
import subprocess
import textwrap
import unittest


WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml"


def job_blocks(raw):
    """Read the workflow's top-level job blocks, without a YAML dependency."""
    jobs = raw.split("\njobs:\n", 1)[1]
    starts = list(re.finditer(r"(?m)^  ([A-Za-z0-9_-]+):\s*$", jobs))
    return {match[1]: jobs[match.end():starts[index + 1].start() if index + 1 < len(starts) else len(jobs)]
            for index, match in enumerate(starts)}


class RequiredStatusTest(unittest.TestCase):
    def setUp(self):
        self.jobs = job_blocks(WORKFLOW.read_text())

    def test_old_status_depends_on_build_and_complete_api_coverage(self):
        gate = self.jobs["test"]
        needs = re.search(r"(?m)^    needs: \[([^\]]+)\]$", gate)
        self.assertIsNotNone(needs)
        self.assertEqual([job.strip() for job in needs[1].split(",")], ["build-checks", "api-coverage", "hosted-workers"])
        self.assertRegex(gate, r"(?m)^    if: always\(\)$")
        self.assertIn("BUILD_CHECKS_RESULT: ${{ needs.build-checks.result }}", gate)
        self.assertIn("API_COVERAGE_RESULT: ${{ needs.api-coverage.result }}", gate)
        self.assertRegex(self.jobs["api-coverage"], r"(?m)^    needs: api-race$")
        self.assertIn("--verify api-race-results --count 4", self.jobs["api-coverage"])

    def test_expensive_checks_and_api_shards_remain_parallel(self):
        for name in ("build-checks", "api-race"):
            self.assertNotRegex(self.jobs[name], r"(?m)^    needs:")
        self.assertIn("scripts/test-api-shard.py --other-packages", self.jobs["build-checks"])
        self.assertIn("scripts/check-ci-gate_test.py", self.jobs["build-checks"])
        self.assertIn("scripts/test-api-shard.py --shard", self.jobs["api-race"])
        self.assertIn("max-parallel: 4", self.jobs["api-race"])

    def test_gate_rejects_failed_cancelled_and_skipped_dependency_results(self):
        body = self.jobs["test"].split("        run: |\n", 1)[1]
        command = textwrap.dedent(body)
        for build in ("success", "failure", "cancelled", "skipped"):
            for api in ("success", "failure", "cancelled", "skipped"):
                with self.subTest(build=build, api=api):
                    environment = dict(os.environ, BUILD_CHECKS_RESULT=build, API_COVERAGE_RESULT=api)
                    result = subprocess.run(["bash", "-e", "-c", command], env=environment, capture_output=True)
                    self.assertEqual(result.returncode == 0, build == api == "success")


if __name__ == "__main__":
    unittest.main()
