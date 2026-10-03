#!/usr/bin/env python3
"""Fail a configured integration suite when fixtures silently skip."""

import argparse
import json
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--require-name", default="")
parser.add_argument("--require-test", action="append", default=[])
args = parser.parse_args()
events = [json.loads(line) for line in sys.stdin if line.strip()]
skipped = [event["Test"] for event in events if event.get("Action") == "skip"]
failed = [event.get("Test", event.get("Package")) for event in events if event.get("Action") == "fail"]
passed = [event["Test"] for event in events if event.get("Action") == "pass" and "Test" in event]
required = [test for test in passed if args.require_name.lower() in test.lower()]
missing = sorted(set(args.require_test) - set(passed))
for event in events:
    if event.get("Action") == "output" and any(word in event.get("Output", "") for word in ["FAIL", "panic:"]):
        print(event["Output"], end="")
if skipped or failed or not passed or missing or args.require_name and not required:
    print(f"Integration fixtures failed: skipped={skipped}, failed={failed}, missing={missing}, required-name passes={len(required)}", file=sys.stderr)
    sys.exit(1)
print(f"Integration checks passed: {len(passed)} tests, {len(required)} required-name fixtures, no skips.")
if args.require_test:
    print(f"All {len(set(args.require_test))} required fixtures passed.")
