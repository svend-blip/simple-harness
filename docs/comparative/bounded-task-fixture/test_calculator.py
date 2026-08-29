# example-project/test_calculator.py — the Simple Harness e2e fixture tests.
#
# One test, one assertion, one failure on the pristine fixture. The test
# exercises the `add(a, b)` function from `calculator.py`; the planted
# defect (`return a - b` instead of `a + b`) causes the assertion
# `add(2, 3) == 5` to FAIL with `assert -1 == 5` (or similar). The
# e2e acceptance runner in `scripts/e2e-coding.sh` (handoff 040)
# confirms the model patches the fixture + re-runs pytest + observes a
# clean exit before declaring success.


from calculator import add


def test_add():
    assert add(2, 3) == 5