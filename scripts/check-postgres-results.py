#!/usr/bin/env python3
"""Fail CI when a configured persistence fixture silently skips."""

import json
import sys

events = [json.loads(line) for line in sys.stdin if line.strip()]
skipped = [event["Test"] for event in events if event.get("Action") == "skip"]
failed = [event.get("Test", event.get("Package")) for event in events if event.get("Action") == "fail"]
passed = [event["Test"] for event in events if event.get("Action") == "pass" and "Test" in event]
postgres = [test for test in passed if "postgres" in test.lower()]
for event in events:
    if event.get("Action") == "output" and any(word in event.get("Output", "") for word in ["FAIL", "panic:"]):
        print(event["Output"], end="")
if skipped or failed or not postgres:
    print(f"PostgreSQL fixtures failed: skipped={skipped}, failed={failed}, PostgreSQL passes={len(postgres)}", file=sys.stderr)
    sys.exit(1)
print(f"Persistence checks passed: {len(passed)} tests, {len(postgres)} PostgreSQL fixtures, no skips.")
