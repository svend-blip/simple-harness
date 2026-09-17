#!/usr/bin/env python3
"""One benchmark arm's figures, read out of the harness's JSONL sidecar.

Everything printed here is a number the harness emitted, not one this script
computed from another one — except the totals, which are sums of those.
"""
import json
import sys


def main() -> int:
    label, path, rc, runtime, limit = sys.argv[1:6]
    calls = input_tokens = 0
    max_prompt = 0
    reduced = 0
    overflow = False
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            kind = rec.get("event") or rec.get("type")
            if kind == "model_request":
                calls += 1
            usage = rec.get("usage") or {}
            if usage:
                n = usage.get("prompt_tokens", 0)
                input_tokens += n
                max_prompt = max(max_prompt, n)
            status = str(rec.get("status", ""))
            if status.startswith("CONTEXT_REDUCED"):
                reduced += 1
            if status.startswith("CONTEXT_BUDGET_EXCEEDED"):
                overflow = True
    if calls == 0:
        # A row of zeros looks like a result. It is not one: the arm never
        # reached the model, and printing it beside a real arm would invite
        # a comparison between a measurement and a failure.
        print(f"{label:<12}{'DID NOT RUN — exit ' + rc:>54}")
        return 1
    mark = "over" if max_prompt > int(limit) else ""
    print(f"{label:<12}{calls:>10}{input_tokens:>12,}{max_prompt:>13,}{mark:<1}"
          f"{reduced:>10}{runtime:>9}s{rc:>9}"
          + ("  BUDGET FAILURE" if overflow else ""))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
