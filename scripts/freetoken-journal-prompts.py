#!/usr/bin/env python3
"""Prompt sizes per model request, read from FreeToken's scheduler journal.

usage: journal_prompts.py <since> <until>     (journalctl time strings)

A request's prompt = #cached-token of its first prefill line + the sum of
#new-token over its prefill lines (a long prompt is prefilled in chunks).
Consecutive prefill lines are chunks of one request; a 'Decode batch' line ends it.
"""
import re
import subprocess
import sys

since, until = sys.argv[1], sys.argv[2]
out = subprocess.run(
    ["journalctl", "--user", "-t", "ft",
     "--since", since, "--until", until, "--no-pager", "-o", "cat"],
    capture_output=True, text=True, check=True).stdout

prefill = re.compile(r"Prefill batch, #new-seq: \d+, #new-token: (\d+), #cached-token: (\d+)")
prompts, prefilling = [], False
for line in out.splitlines():
    m = prefill.search(line)
    if m:
        size = int(m.group(1)) + int(m.group(2))
        if prefilling:
            prompts[-1] += size        # a later chunk of the same prefill
        else:
            prompts.append(size)       # a request starts
            prefilling = True
    elif "Decode batch" in line:
        prefilling = False             # prefill is over; the next one is a new request

if not prompts:
    print("no requests in window")
    sys.exit(1)
print(f"requests        {len(prompts)}")
print(f"input tokens    {sum(prompts)}")
print(f"largest prompt  {max(prompts)}")
print(f"prompt sizes    {prompts}")
