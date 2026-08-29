# example-project/calculator.py — the Simple Harness e2e fixture.
#
# This module is the canonical SCOPE §40 fixture material for the
# Run 011 / handoff 039 acceptance slice. It exposes a single function
# `add(a, b)` with a PLANTED DEFECT: the implementation returns `a - b`
# instead of `a + b`, so the canonical pytest
#
#     def test_add():
#         assert add(2, 3) == 5
#
# in `example-project/test_calculator.py` FAILS on the pristine fixture.
# The e2e acceptance runner (`scripts/e2e-coding.sh`, lands in handoff 040)
# instructs the model to inspect the failing test, patch the defect,
# re-run pytest, and confirm a clean exit — that is the vertical slice.
#
# The planted defect is the ONLY failure surface. No other tests, no
# other functions, no other failure modes — the runner asserts from
# events + exit code + workspace state: tests ran, a patch/write landed,
# tests re-ran, final exit 0 (GOAL §2 bound decision 2).


def add(a, b):
    # BUG: should be `return a + b`. Planted for the e2e slice.
    return a - b