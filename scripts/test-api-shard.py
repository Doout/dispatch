#!/usr/bin/env python3
"""Partition Go-discovered API roots and check their execution, without test lists.

The ordinary suite retains its four existing opt-in Docker fixtures. Their
dedicated CI jobs still use check-test-results.py to reject every skip.
Issue #96 timing baseline: PR #95, run 37135882037, API 2299.959 seconds.
Compare runner timings against primary release CI run 37131689522.
"""

import argparse
from collections import Counter
import json
import math
from pathlib import Path
import subprocess
import sys
import unicodedata


PACKAGE = "github.com/doout/dispatch/internal/api"
EXISTING_OPT_IN_FIXTURES = {
    "TestBuiltinDockerServiceProvisionEndToEnd",
    "TestInfrastructureManagedRuntimeIntegration",
    "TestPackagedMockProviderCLIAndMCPAcceptanceIntegration",
    "TestTemporaryEnvironmentDockerRestartExpiryIntegration",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def json_objects(raw):
    """Read Go's concatenated JSON objects, rejecting trailing malformed data."""
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(raw):
        if raw[offset].isspace():
            offset += 1
            continue
        value, offset = decoder.raw_decode(raw, offset)
        require(isinstance(value, dict), "Go JSON event must be an object")
        yield value


def is_identifier(name):
    return bool(name) and all(
        char == "_" or unicodedata.category(char).startswith("L")
        or index > 0 and unicodedata.category(char) == "Nd"
        for index, char in enumerate(name)
    )


def discovered_roots(events):
    roots = []
    package_passes = 0
    for event in events:
        require(event.get("Package") == PACKAGE, "Unexpected discovery package")
        require(event.get("Action") not in {"fail", "skip"}, "Go discovery failed or skipped")
        require("Test" not in event, "Discovery unexpectedly executed a test")
        if event.get("Action") == "pass":
            package_passes += 1
        if event.get("Action") == "output":
            for line in event.get("Output", "").splitlines():
                if line.startswith(("Test", "Example", "Fuzz")):
                    require(is_identifier(line), f"Invalid discovered root: {line!r}")
                    roots.append(line)
    require(package_passes == 1, "Discovery must finish exactly one package")
    require(roots, "Discovery returned no Test, Example or Fuzz roots")
    require(len(roots) == len(set(roots)), "Duplicate discovered roots")
    return sorted(roots)


def validate_assignments(roots, shards):
    require(2 <= len(shards) <= 4, "Use two to four API shards")
    require(roots == sorted(set(roots)) and roots, "Discovery roots must be unique and sorted")
    for shard in shards:
        require(isinstance(shard, list) and shard, "Empty or invalid shard")
        require(all(isinstance(root, str) and is_identifier(root) for root in shard), "Invalid shard root")
    assigned = Counter(root for shard in shards for root in shard)
    require(assigned == Counter(roots), "Shard assignments must cover every discovered root exactly once")


def partition(roots, count):
    require(2 <= count <= 4, "Use two to four API shards")
    shards = [roots[index::count] for index in range(count)]
    validate_assignments(roots, shards)
    return shards


def selection_pattern(roots):
    require(roots and all(is_identifier(root) for root in roots), "Invalid regex roots")
    # Go identifiers contain no regexp metacharacters or slash separators. An
    # anchored root pattern runs all its subtests and fuzz seeds in Go's -run.
    return "^(" + "|".join(roots) + ")$"


def check_results(events, assigned):
    runs, outcomes, elapsed = Counter(), Counter(), {}
    package_passes = 0
    seconds = None
    for event in events:
        require(event.get("Package") == PACKAGE, "Unexpected result package")
        action, test = event.get("Action"), event.get("Test")
        require(action != "fail", f"Go test failure: {test or PACKAGE}")
        if action == "skip":
            require(test in EXISTING_OPT_IN_FIXTURES, f"Unexpected skipped test: {test}")
        if test:
            root = test.split("/", 1)[0]
            require(root in assigned, f"Unassigned test executed: {test}")
            if test == root:
                if action == "run":
                    runs[root] += 1
                if action in {"pass", "skip"}:
                    outcomes[root] += 1
                    elapsed[root] = event.get("Elapsed", 0)
        elif action == "pass":
            package_passes += 1
            seconds = event.get("Elapsed")
    expected = Counter(assigned)
    require(runs == expected, "Assigned roots must each run exactly once")
    require(outcomes == expected, "Assigned roots must each finish exactly once")
    require(package_passes == 1 and isinstance(seconds, (int, float)), "Missing successful package result")
    return seconds, elapsed


def verify_summaries(summaries, count):
    require(2 <= count <= 4, "Use two to four API shards")
    require(len(summaries) == count, "Missing or duplicate shard summaries")
    ordered = sorted(summaries, key=lambda item: item["shard"])
    require([item["shard"] for item in ordered] == list(range(count)), "Invalid shard indices")
    roots = ordered[0]["discovered"]
    for item in ordered:
        require(item["package"] == PACKAGE and item["count"] == count, "Summary configuration mismatch")
        require(item["discovered"] == roots, "Shard discovery sets differ")
        require(set(item["rootSeconds"]) == set(item["roots"]), "Missing execution evidence")
        require(isinstance(item["seconds"], (int, float)) and math.isfinite(item["seconds"])
                and item["seconds"] >= 0, "Invalid package timing")
    validate_assignments(roots, [item["roots"] for item in ordered])
    return ordered


def run_shard(index, count, output):
    require(0 <= index < count, "Shard index is out of range")
    output.mkdir(parents=True, exist_ok=True)
    discovery = subprocess.run(
        ["go", "test", "-race", "-count=1", "-timeout", "40m", "-list", ".", "-json", "./internal/api"],
        text=True, capture_output=True, check=True,
    )
    (output / f"discovery-{index}.jsonl").write_text(discovery.stdout)
    roots = discovered_roots(json_objects(discovery.stdout))
    shards = partition(roots, count)
    assigned = shards[index]
    (output / f"assignments-{index}.json").write_text(json.dumps(shards, indent=2) + "\n")
    print(f"API shard {index + 1}/{count}: {len(assigned)} of {len(roots)} discovered roots", flush=True)
    command = ["go", "test", "-race", "-count=1", "-timeout", "40m", "-json", "./internal/api", "-run", selection_pattern(assigned)]
    with (output / f"results-{index}.jsonl").open("w") as log:
        process = subprocess.Popen(command, text=True, stdout=subprocess.PIPE)
        for line in process.stdout:
            log.write(line)
            event = json.loads(line)
            if event.get("Action") == "output":
                print(event.get("Output", ""), end="", flush=True)
        require(process.wait() == 0, "API shard go test failed")
    events = json_objects((output / f"results-{index}.jsonl").read_text())
    seconds, elapsed = check_results(events, assigned)
    summary = {"package": PACKAGE, "shard": index, "count": count, "discovered": roots,
               "roots": assigned, "seconds": seconds, "rootSeconds": elapsed}
    (output / f"summary-{index}.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"API shard {index + 1}/{count}: every assigned root completed, package {seconds:.3f}s")


def run_other_packages():
    result = subprocess.run(["go", "list", "-json", "./..."], text=True, capture_output=True, check=True)
    packages = [item["ImportPath"] for item in json_objects(result.stdout)]
    require(packages.count(PACKAGE) == 1 and len(set(packages)) == len(packages), "Invalid package discovery")
    packages.remove(PACKAGE)
    require(packages, "No non-API packages discovered")
    subprocess.run(["go", "test", "-race", "-count=1", "-timeout", "40m", *packages], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int)
    parser.add_argument("--count", type=int, default=4)
    parser.add_argument("--output", type=Path, default=Path("api-race-results"))
    parser.add_argument("--verify", type=Path)
    parser.add_argument("--other-packages", action="store_true")
    args = parser.parse_args()
    require(sum((args.shard is not None, args.verify is not None, args.other_packages)) == 1,
            "Select exactly one of --shard, --verify or --other-packages")
    require(2 <= args.count <= 4, "Use two to four API shards")
    if args.other_packages:
        run_other_packages()
    elif args.verify:
        summaries = [json.loads(path.read_text()) for path in args.verify.glob("summary-*.json")]
        for item in verify_summaries(summaries, args.count):
            headroom = (1 - item["seconds"] / 2400) * 100
            print(f"API shard {item['shard'] + 1}/{args.count}: {len(item['roots'])} roots, "
                  f"{item['seconds']:.3f}s, {headroom:.1f}% below unchanged 40m timeout")
        print(f"Complete API coverage: {len(summaries[0]['discovered'])} roots, exactly once")
    else:
        run_shard(args.shard, args.count, args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"API race coverage failed: {error}", file=sys.stderr)
        sys.exit(1)
