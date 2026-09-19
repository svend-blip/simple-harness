#!/usr/bin/env python3
"""Where one benchmark arm's wall time went, read from its JSONL sidecar.

usage: benchmark-timing.py <sidecar.jsonl> [--calls]

Every model call is a model_request event, a first STREAMING status (time
to first token) and a usage event (end of the call, with the runtime's
token counts). A call made while the status is COMPACTING is a compaction
inference; every other call is a working turn. What is left of the wall
time — tool execution, the harness itself — is reported as "outside model
calls".
"""
import json
import sys
from datetime import datetime


def ts(rec):
    raw = rec["timestamp"]
    # nanoseconds -> microseconds, which is what fromisoformat reads
    head, _, frac = raw.rstrip("Z").partition(".")
    return datetime.fromisoformat(f"{head}.{(frac + '000000')[:6]}+00:00").timestamp()


def main() -> int:
    path = sys.argv[1]
    show_calls = "--calls" in sys.argv[2:]
    calls, stack, compacting = [], [], False
    first = last = None
    with open(path) as fh:
        for line in fh:
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if "timestamp" not in rec:
                continue
            t = ts(rec)
            first = t if first is None else first
            last = t
            kind = rec.get("event")
            status = str(rec.get("status", ""))
            if kind == "status" and status == "COMPACTING":
                compacting = True
            if kind == "model_request":
                # A compaction runs inside the working call that needed
                # it: request(work), COMPACTING, request(compaction),
                # usage(compaction), STREAMING, usage(work). Hence a stack.
                stack.append({"start": t, "ttft": None, "nested": 0.0,
                              "kind": "compaction" if compacting else "work"})
                compacting = False
            elif kind == "status" and status == "STREAMING" and stack and stack[-1]["ttft"] is None:
                stack[-1]["ttft"] = t - stack[-1]["start"] - stack[-1]["nested"]
            elif kind == "usage" and stack:
                cur = stack.pop()
                u = rec.get("usage") or {}
                span = t - cur["start"]
                cur.update(total=span - cur["nested"], prompt=u.get("prompt_tokens", 0),
                           completion=u.get("completion_tokens", 0),
                           reasoning=u.get("reasoning_tokens", 0))
                if stack:
                    stack[-1]["nested"] += span
                calls.append(cur)
    if not calls:
        print("no completed model calls in the sidecar")
        return 1

    wall = last - first
    print(f"{'kind':<12}{'calls':>6}{'time s':>9}{'share':>7}{'ttft s':>9}{'gen s':>8}"
          f"{'prompt tok':>12}{'output tok':>12}{'reasoning':>11}")
    in_calls = 0.0
    for kind in ("work", "compaction"):
        rows = [c for c in calls if c["kind"] == kind]
        if not rows:
            continue
        total = sum(c["total"] for c in rows)
        ttft = sum(c["ttft"] or 0 for c in rows)
        in_calls += total
        print(f"{kind:<12}{len(rows):>6}{total:>9.1f}{total / wall:>7.0%}{ttft:>9.1f}{total - ttft:>8.1f}"
              f"{sum(c['prompt'] for c in rows):>12,}{sum(c['completion'] for c in rows):>12,}"
              f"{sum(c['reasoning'] for c in rows):>11,}")
    print(f"{'outside model calls':<27}{wall - in_calls:>9.1f}{(wall - in_calls) / wall:>7.0%}")
    print(f"{'wall':<27}{wall:>9.1f}")
    if show_calls:
        print()
        for i, c in enumerate(calls, 1):
            print(f"{i:>3} {c['kind']:<11}{c['total']:>7.1f}s  ttft {c['ttft'] or 0:>5.1f}s  "
                  f"prompt {c['prompt']:>7,}  output {c['completion']:>6,}  reasoning {c['reasoning']:>6,}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
