#!/usr/bin/env python3
"""Reject common credential formats in public provider examples and fixtures."""

from pathlib import Path
import re
import sys

root = Path(__file__).resolve().parent.parent
patterns = [
    re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----"),
    re.compile(r"\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{40,}\b"),
    re.compile(r"\bAKIA[A-Z0-9]{16}\b"),
    re.compile(r"https?://[^\s/@:]+:[^\s/@]+@"),
]
paths = [root / "docs/provider-api.md"]
for folder in ["examples/providers", "internal/provider", "cmd/dispatch-provider-mock", "cmd/dispatch-provider-conformance"]:
    paths.extend(path for path in (root / folder).rglob("*") if path.is_file())
failed = []
for path in paths:
    content = path.read_text()
    if any(pattern.search(content) for pattern in patterns):
        failed.append(str(path.relative_to(root)))
if failed:
    print("Credential-like content found in public provider files:", file=sys.stderr)
    print("\n".join(failed), file=sys.stderr)
    sys.exit(1)
print("Public provider fixtures contain no detected credential formats.")
