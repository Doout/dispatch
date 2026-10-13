#!/usr/bin/env python3
"""Shard every non-API Go test root and verify complete execution evidence."""

import argparse
from collections import Counter
import importlib.util
import json
from pathlib import Path
import subprocess
import sys


spec = importlib.util.spec_from_file_location("api_shards", Path(__file__).with_name("test-api-shard.py"))
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)
BASELINE = Path(__file__).with_name("go-package-timings.json")


def discover(events):
    roots, finished = {}, Counter()
    for event in events:
        package, action = event.get("Package"), event.get("Action")
        api.require(action not in {"fail", "build-fail"}, "Go discovery failed or skipped")
        if not package:
            continue  # Compiler output has no Package field.
        api.require("Test" not in event, "Discovery unexpectedly executed a test")
        roots.setdefault(package, [])
        if action == "pass":
            finished[package] += 1
        if action == "output":
            for line in event.get("Output", "").splitlines():
                if line.startswith(("Test", "Example", "Fuzz")):
                    api.require(api.is_identifier(line), f"Invalid discovered root: {line!r}")
                    roots[package].append(line)
    api.require(roots, "No packages discovered")
    for package, names in roots.items():
        api.require(len(names) == len(set(names)), f"Duplicate roots in {package}")
        api.require(not names or finished[package] == 1, f"Incomplete discovery for {package}")
        roots[package] = sorted(names)
    return dict(sorted(roots.items()))


def partition(roots, count, timings):
    api.require(2 <= count <= 16, "Use two to sixteen Go shards")
    weights = Counter()
    for package, names in roots.items():
        for name in names:
            weights[name] += timings.get(package, 10) / len(names)
    api.require(len(weights) >= count, "Not enough roots for nonempty shards")
    assignments, loads = [[] for _ in range(count)], [0] * count
    # A root name must have one shard across all packages: Go applies -run to
    # every selected package. Group repeated names to prevent cross-shard runs.
    for name in sorted(weights, key=lambda name: (-weights[name], name)):
        index = min(range(count), key=lambda index: (loads[index], index))
        assignments[index].append(name)
        loads[index] += weights[name]
    return [sorted(names) for names in assignments]


def expected_roots(roots, names):
    return Counter((package, name) for package, tests in roots.items() for name in tests if name in names)


def check_results(events, expected, packages):
    runs, outcomes, finished, elapsed = Counter(), Counter(), Counter(), {}
    for event in events:
        action, package, test = event.get("Action"), event.get("Package"), event.get("Test")
        api.require(action not in {"fail", "build-fail"}, f"Go test failed: {package} {test or ''}")
        if test:
            root = test.split("/", 1)[0]
            api.require((package, root) in expected, f"Unassigned test executed: {package} {test}")
            if root == test:
                if action == "run":
                    runs[(package, root)] += 1
                if action in {"pass", "skip"}:
                    outcomes[(package, root)] += 1
        elif action in {"pass", "skip"}:
            api.require(package in packages, f"Unexpected package: {package}")
            finished[package] += 1
            elapsed[package] = event.get("Elapsed", 0)
    api.require(runs == expected, "Each assigned root must run exactly once")
    api.require(outcomes == expected, "Each assigned root must finish exactly once")
    api.require(finished == Counter(packages), "Every discovered package must finish exactly once")
    return elapsed


def verify(summaries, count):
    api.require(len(summaries) == count, "Missing or duplicate Go shard summaries")
    ordered = sorted(summaries, key=lambda item: item["shard"])
    api.require([item["shard"] for item in ordered] == list(range(count)), "Invalid Go shard indices")
    roots, assigned = ordered[0]["discovered"], Counter()
    for item in ordered:
        api.require(item["count"] == count and item["discovered"] == roots, "Go discovery sets differ")
        expected = expected_roots(roots, item["names"])
        api.require(Counter(tuple(root) for root in item["executed"]) == expected, "Missing Go execution evidence")
        api.require(set(item["packageSeconds"]) == {package for package, name in expected},
                    "Missing successful package evidence")
        assigned.update(expected)
    api.require(assigned == expected_roots(roots, {name for names in roots.values() for name in names}),
                "Go shards must cover every discovered root exactly once")
    return ordered


def run_shard(index, count, output):
    api.require(0 <= index < count, "Shard index is out of range")
    output.mkdir(parents=True, exist_ok=True)
    listing = subprocess.run(["go", "list", "./..."], text=True, capture_output=True, check=True)
    packages = listing.stdout.splitlines()
    api.require(packages.count(api.PACKAGE) == 1 and len(set(packages)) == len(packages), "Invalid package discovery")
    packages.remove(api.PACKAGE)
    discovery = subprocess.run(["go", "test", "-race", "-p", "2", "-count=1", "-timeout", "8m",
                                "-list", ".", "-json", *packages], text=True, capture_output=True, check=True)
    (output / f"discovery-{index}.jsonl").write_text(discovery.stdout)
    roots = discover(api.json_objects(discovery.stdout))
    api.require(set(roots) == set(packages), "Discovery omitted packages")
    timings = json.loads(BASELINE.read_text())["packageSeconds"]
    names = partition(roots, count, timings)[index]
    expected = expected_roots(roots, names)
    selected = {package for package, name in expected}
    # Discovery already compiles packages without selected roots. Avoid linking
    # them again, and start the longest suites first to reduce the final tail.
    selected = sorted(selected, key=lambda package: (
        -timings.get(package, 10) * sum(p == package for p, name in expected) / len(roots[package]), package))
    print(f"Go shard {index + 1}/{count}: {len(expected)} roots in {len(selected)} packages", flush=True)
    command = ["go", "test", "-race", "-p", "2", "-count=1", "-timeout", "8m", "-json",
               "-run", api.selection_pattern(names), *selected]
    with (output / f"results-{index}.jsonl").open("w") as log:
        process = subprocess.Popen(command, text=True, stdout=subprocess.PIPE)
        for line in process.stdout:
            log.write(line)
            event = json.loads(line)
            if event.get("Action") == "output":
                print(event.get("Output", ""), end="", flush=True)
        api.require(process.wait() == 0, "Go shard test command failed")
    elapsed = check_results(api.json_objects((output / f"results-{index}.jsonl").read_text()), expected, selected)
    summary = {"shard": index, "count": count, "discovered": roots, "names": names,
               "executed": sorted(expected), "packageSeconds": elapsed}
    (output / f"summary-{index}.json").write_text(json.dumps(summary, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int)
    parser.add_argument("--count", type=int, default=6)
    parser.add_argument("--output", type=Path, default=Path("go-race-results"))
    parser.add_argument("--verify", type=Path)
    args = parser.parse_args()
    api.require(2 <= args.count <= 16, "Use two to sixteen Go shards")
    api.require((args.shard is not None) != (args.verify is not None), "Select --shard or --verify")
    if args.verify:
        summaries = [json.loads(path.read_text()) for path in args.verify.glob("summary-*.json")]
        ordered = verify(summaries, args.count)
        print(f"Complete Go coverage: {sum(len(item['executed']) for item in ordered)} roots, exactly once")
    else:
        run_shard(args.shard, args.count, args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"Go race coverage failed: {error}", file=sys.stderr)
        sys.exit(1)
