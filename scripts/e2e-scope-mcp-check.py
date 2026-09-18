#!/usr/bin/env python3
"""Assertions for scripts/e2e-scope-mcp.sh, read from the run directory."""
import json
import os
import sys

tmp = sys.argv[1]
failures = []


def events(label):
    out = []
    for line in open(f"{tmp}/{label}.jsonl"):
        try:
            out.append(json.loads(line))
        except ValueError:
            pass
    return out


def check(name, ok, detail=""):
    print(("ok   " if ok else "FAIL ") + name + (f" — {detail}" if detail and not ok else ""))
    if not ok:
        failures.append(name)


r1 = events("r1")
results = {e["call_id"]: e for e in r1 if e.get("event") == "tool_result"}
status = {k: v.get("tool_result_status") for k, v in results.items()}
check("1 listing and five tool rounds", len(results) == 5, str(status))

reqs = [json.loads(line) for line in open(f"{tmp}/r1.req")]
tools = {t["function"]["name"]: t["function"]["parameters"] for t in reqs[-1]["tools"]}
goals = tools.get("set_goals", {}).get("properties", {}).get("goals", {})
item_props = goals.get("items", {}).get("properties", {})
check("2 model sees the item shape and the enum",
      "title" in item_props and "enum" in item_props.get("status", {}), json.dumps(goals)[:200])

history = reqs[-1]["messages"]
pairs_ok = True
for i, m in enumerate(history):
    if m["role"] == "assistant" and m.get("tool_calls"):
        nxt = history[i + 1] if i + 1 < len(history) else {}
        pairs_ok &= nxt.get("role") == "tool" and nxt.get("tool_call_id") == m["tool_calls"][0]["id"]
check("3 every tool call is followed by its own result", pairs_ok)

check("4 server-side error reaches the model as a failure",
      status.get("call_rt_3") == "error" and "unknown goal id" in results["call_rt_3"].get("content", ""))
check("5 schema violation rejected before the server",
      status.get("call_rt_4") == "error" and "schema" in results["call_rt_4"].get("content", ""))

r2 = [e for e in events("r2") if e.get("event") == "tool_result"]
check("6 a new harness process reads the state the first one wrote",
      len(r2) == 1 and "Round-trip proof" in r2[0].get("content", "") and "g1" in r2[0].get("content", ""))

check("7 the server ran in the workspace, not where the harness was launched",
      os.path.exists(f"{tmp}/ws/.scope-mcp/state.db") and not os.path.exists(f"{tmp}/elsewhere/.scope-mcp"),
      f"ws={os.path.exists(tmp + '/ws/.scope-mcp')} elsewhere={os.path.exists(tmp + '/elsewhere/.scope-mcp')}")

print(f"\n{7 - len(failures)}/7 passed")
sys.exit(1 if failures else 0)
