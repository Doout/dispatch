#!/usr/bin/env python3
"""Coverage and Go selection checks for the non-API race matrix."""

import copy
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('go_shards', Path(__file__).with_name('test-go-shard.py'))
shards = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shards)


class CoverageTest(unittest.TestCase):
    def setUp(self):
        self.roots = {'first': ['TestFirst', 'TestShared'], 'second': ['FuzzSeeds', 'TestShared'], 'empty': []}
        self.assignments = shards.partition(self.roots, 2, {'first': 30, 'second': 10})
        self.summaries = [{'shard': index, 'count': 2, 'discovered': self.roots, 'names': names,
                           'executed': sorted(shards.expected_roots(self.roots, names)),
                           'packageSeconds': dict.fromkeys(self.roots, 1)}
                          for index, names in enumerate(self.assignments)]

    def test_shared_root_names_have_one_shard_and_future_roots_enter_coverage(self):
        self.assertEqual(sum('TestShared' in names for names in self.assignments), 1)
        self.assertEqual(shards.verify(self.summaries, 2), self.summaries)
        roots = copy.deepcopy(self.roots)
        roots['newpackage'] = ['TestFuture']
        assignments = shards.partition(roots, 2, {})
        self.assertEqual(sum('TestFuture' in names for names in assignments), 1)

    def test_missing_duplicate_or_drifted_coverage_fails(self):
        missing = copy.deepcopy(self.summaries); missing[0]['executed'].pop()
        duplicate = copy.deepcopy(self.summaries); duplicate[1] = duplicate[0]
        drift = copy.deepcopy(self.summaries); drift[1]['discovered'] = {'new': ['TestNew']}
        for invalid in (self.summaries[:-1], missing, duplicate, drift):
            with self.subTest(summaries=invalid), self.assertRaises(ValueError):
                shards.verify(invalid, 2)

    def test_missing_duplicate_unassigned_or_failed_execution_fails(self):
        expected = shards.expected_roots(self.roots, self.assignments[0])
        events = [event for package, name in expected for event in
                  [{'Package': package, 'Action': 'run', 'Test': name},
                   {'Package': package, 'Action': 'pass', 'Test': name}]]
        events += [{'Package': package, 'Action': 'pass'} for package in self.roots]
        shards.check_results(events, expected, list(self.roots))
        for invalid in (events[1:], events + events[:1],
                        events + [{'Package': 'first', 'Action': 'run', 'Test': 'TestUnassigned'}],
                        events + [{'Package': 'first', 'Action': 'fail'}], events[:-1]):
            with self.subTest(events=invalid), self.assertRaises(ValueError):
                shards.check_results(invalid, expected, list(self.roots))

    def test_go_discovery_and_execution_across_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'go.mod').write_text('module shardfixture\n\ngo 1.24\n')
            for package, body in {'first': 'func TestFirst(t *testing.T) {}',
                                  'second': 'func TestSecond(t *testing.T) {}'}.items():
                path = root / package; path.mkdir()
                (path / 'fixture_test.go').write_text('package ' + package + '\nimport "testing"\n' +
                    body + '\nfunc TestShared(t *testing.T) { t.Run("child", func(t *testing.T) {}) }\n')
            (root / 'empty').mkdir()
            (root / 'empty/empty.go').write_text('package empty\n')
            env = dict(os.environ, GOWORK='off')
            listing = subprocess.run(['go', 'test', '-list', '.', '-json', './...'], cwd=root,
                                     env=env, text=True, capture_output=True, check=True)
            roots = shards.discover(shards.api.json_objects(listing.stdout))
            assignments = shards.partition(roots, 2, {})
            for names in assignments:
                result = subprocess.run(['go', 'test', '-count=1', '-json', '-run',
                    shards.api.selection_pattern(names), './...'], cwd=root, env=env,
                    text=True, capture_output=True, check=True)
                shards.check_results(shards.api.json_objects(result.stdout),
                                     shards.expected_roots(roots, names), list(roots))


if __name__ == '__main__':
    unittest.main()
