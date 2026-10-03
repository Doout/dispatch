#!/usr/bin/env python3
"""Coverage failures must stop CI; Go itself checks root/subtest selection."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("test-api-shard.py")
spec = importlib.util.spec_from_file_location("api_shards", SCRIPT)
shards = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shards)


def event(action, test=None, **extra):
    value = {"Package": shards.PACKAGE, "Action": action, **extra}
    if test is not None:
        value["Test"] = test
    return value


def discovery(roots):
    return [event("start"), *[event("output", Output=root + "\n") for root in roots],
            event("pass", Elapsed=0.1)]


def results(roots):
    return [event("start"), *[value for root in roots for value in
            [event("run", root), event("pass", root, Elapsed=0.1)]], event("pass", Elapsed=0.2)]


class AssignmentsTest(unittest.TestCase):
    def setUp(self):
        self.roots = sorted(["TestAlpha", "TestBeta", "TestCase", "TestCaseSuffix",
                             "TestCASE", "Test_New", "Example", "FuzzSeeds"])

    def test_discovery_and_future_roots_enter_all_shard_counts(self):
        for count in (2, 3, 4):
            roots = shards.discovered_roots(discovery(self.roots + ["TestFutureRoot"]))
            assignments = shards.partition(roots, count)
            self.assertEqual(sorted(root for part in assignments for root in part), roots)
            shards.validate_assignments(roots, assignments)

    def test_missing_duplicate_and_empty_assignments_fail(self):
        parts = shards.partition(self.roots, 4)
        missing = copy.deepcopy(parts)
        missing[0].pop()
        duplicate = copy.deepcopy(parts)
        duplicate[1].append(duplicate[0][0])
        empty = copy.deepcopy(parts)
        empty[0] = []
        for invalid in (missing, duplicate, empty):
            with self.subTest(assignments=invalid), self.assertRaises(ValueError):
                shards.validate_assignments(self.roots, invalid)

    def test_invalid_discovery_fails(self):
        for invalid in (discovery([]), discovery(["TestA", "TestA"]),
                        discovery(["TestA|TestB"]), discovery(["TestA/subtest"]),
                        [event("output", Output="TestA\n"), event("fail")],
                        [event("run", "TestA"), event("pass")],
                        [{"Action": "pass", "Package": "foreign"}]):
            with self.subTest(events=invalid), self.assertRaises(ValueError):
                shards.discovered_roots(invalid)
        with self.assertRaises(ValueError):
            list(shards.json_objects('{}\nnot-json'))

    def test_invalid_cli_and_empty_shards_fail(self):
        for args in ([], ["--shard", "-1"], ["--shard", "4"], ["--shard", "0", "--count", "1"],
                     ["--shard", "0", "--count", "5"], ["--shard", "0", "--other-packages"],
                     ["--shard", "wrong"]):
            with self.subTest(args=args):
                result = subprocess.run(["python3", str(SCRIPT), *args], capture_output=True)
                self.assertNotEqual(result.returncode, 0)
        with self.assertRaises(ValueError):
            shards.partition(["TestOnly"], 4)

    def test_exactly_once_execution_and_no_unexpected_skips(self):
        expected = ["TestA", "ExampleA", "FuzzA"]
        seconds, elapsed = shards.check_results(results(expected), expected)
        self.assertEqual(seconds, 0.2)
        self.assertEqual(set(elapsed), set(expected))
        good = results(expected)
        invalids = [results(expected[:-1]), good + [event("run", "TestA")],
                    good + [event("pass", "TestA")], good + [event("run", "TestExtra")],
                    good + [event("skip", "TestA")], good + [event("skip", "TestA/subtest")],
                    good + [event("fail", "TestA/subtest")], good[:-1]]
        for invalid in invalids:
            with self.subTest(events=invalid), self.assertRaises(ValueError):
                shards.check_results(invalid, expected)

    def test_existing_fixture_skips_still_require_execution(self):
        fixture = sorted(shards.EXISTING_OPT_IN_FIXTURES)[0]
        events = [event("run", fixture), event("skip", fixture), event("pass", Elapsed=0.1)]
        shards.check_results(events, [fixture])
        with self.assertRaises(ValueError):
            shards.check_results(events[1:], [fixture])

    def test_complete_matrix_summaries_fail_on_missing_duplicate_or_drift(self):
        parts = shards.partition(self.roots, 4)
        summaries = [{"package": shards.PACKAGE, "shard": index, "count": 4,
                      "discovered": self.roots, "roots": roots, "seconds": 0.1,
                      "rootSeconds": {root: 0.1 for root in roots}}
                     for index, roots in enumerate(parts)]
        self.assertEqual(shards.verify_summaries(summaries, 4), summaries)
        duplicate = copy.deepcopy(summaries)
        duplicate[-1] = duplicate[0]
        drift = copy.deepcopy(summaries)
        drift[0]["discovered"] = self.roots + ["TestFuture"]
        missing_result = copy.deepcopy(summaries)
        missing_result[0]["rootSeconds"] = {}
        for invalid in (summaries[:-1], duplicate, drift, missing_result):
            with self.subTest(summaries=invalid), self.assertRaises(ValueError):
                shards.verify_summaries(invalid, 4)

    def test_every_other_discovered_package_keeps_the_full_race_suite(self):
        packages = ["dispatch/core", shards.PACKAGE, "dispatch/futurepackage"]
        listed = "\n".join(json.dumps({"ImportPath": name}) for name in packages)
        with patch.object(shards.subprocess, "run", return_value=SimpleNamespace(stdout=listed)) as run:
            shards.run_other_packages()
            command = run.call_args_list[-1].args[0]
        self.assertNotIn(shards.PACKAGE, command)
        self.assertEqual(command, ["go", "test", "-race", "-count=1", "-timeout", "40m",
                                   "dispatch/core", "dispatch/futurepackage"])
        for invalid in (packages + ["dispatch/core"], packages[:1], [shards.PACKAGE]):
            listed = "\n".join(json.dumps({"ImportPath": name}) for name in invalid)
            with patch.object(shards.subprocess, "run", return_value=SimpleNamespace(stdout=listed)) as run:
                with self.assertRaises(ValueError):
                    shards.run_other_packages()
                self.assertEqual(run.call_count, 1)


class GoSelectionTest(unittest.TestCase):
    def test_go_regex_keeps_case_prefixes_subtests_examples_and_fuzz_seeds(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / "go.mod").write_text("module shardfixture\n\ngo 1.24\n")
            (path / "selection_test.go").write_text('''package shardfixture
import ("fmt"; "testing")
func TestCase(t *testing.T) { t.Run("TestCaseSuffix", func(t *testing.T) {}) }
func TestCaseSuffix(t *testing.T) {}
func TestCASE(t *testing.T) {}
func FuzzSeeds(f *testing.F) { f.Add("a"); f.Add("b"); f.Fuzz(func(t *testing.T, value string) {}) }
func Example() { fmt.Println("example")
// Output: example
}
''')
            environment = dict(os.environ, GOWORK="off")
            listing = subprocess.run(["go", "test", "-list", ".", "-json", "."], cwd=path,
                                     env=environment, text=True, capture_output=True, check=True)
            events = list(shards.json_objects(listing.stdout))
            for value in events:
                value["Package"] = shards.PACKAGE
            roots = shards.discovered_roots(events)
            self.assertEqual(roots, ["Example", "FuzzSeeds", "TestCASE", "TestCase", "TestCaseSuffix"])
            executed, children = [], []
            for assigned in shards.partition(roots, 2):
                result = subprocess.run(["go", "test", "-count=1", "-json", ".", "-run",
                                         shards.selection_pattern(assigned)], cwd=path,
                                        env=environment, text=True, capture_output=True, check=True)
                events = list(shards.json_objects(result.stdout))
                for value in events:
                    value["Package"] = shards.PACKAGE
                    if value["Action"] == "run":
                        name = value["Test"]
                        (children if "/" in name else executed).append(name)
                shards.check_results(events, assigned)
            self.assertEqual(sorted(executed), roots)
            self.assertEqual(sorted(children), ["FuzzSeeds/seed#0", "FuzzSeeds/seed#1", "TestCase/TestCaseSuffix"])


class IntegrationCheckerTest(unittest.TestCase):
    def test_existing_checker_still_rejects_missing_failed_and_skipped_fixtures(self):
        checker = SCRIPT.with_name("check-test-results.py")
        for terminal, required, success in (("pass", "TestDocker", True),
                                            ("pass", "TestMissing", False),
                                            ("skip", "TestDocker", False),
                                            ("fail", "TestDocker", False)):
            events = [event("run", "TestDocker"), event(terminal, "TestDocker"), event("pass")]
            result = subprocess.run(["python3", str(checker), "--require-test", required],
                                    input="\n".join(json.dumps(value) for value in events),
                                    text=True, capture_output=True)
            self.assertEqual(result.returncode == 0, success, result.stderr)


if __name__ == "__main__":
    unittest.main()
