# The suite on Windows

`go test ./...` first ran on Windows on 2026-09-21 (CI, `windows-latest`). On
Windows the gate is `go vet ./...` — every package and its tests compile — and
the live scope-mcp round trip (`scripts/e2e-scope-mcp.sh`). The full suite
runs there too and is shown, but does not block: 13 tests failed on the first
run, and they are listed here until each is either fixed or moved behind a
build tag with a reason. Linux is a full gate, with `-race`.

## Fixed

| Test | What it was |
|---|---|
| `TestPolicy_WORKSPACE_WRITE_RejectsEscape` | **Product.** The policy looked for `..` followed by the platform's separator, so on Windows `../escape` was allowed (and `sub/../../escape` everywhere). `internal/path.Normalize` still refused the write. Now judged by where the path ends up, both separators, both platforms. |

## Open — may be the product

| Tests | What is seen |
|---|---|
| `TestShell_TimeoutKillsWholeGroup`, `TestShell_TimeoutEscalatesToSIGKILL`, `TestShell_NoOrphanSurvives`, `TestShell_DefaultTimeoutAppliesWhenCallerOmitsTimeoutMs` | A shell command that exceeds its timeout is not ended at the timeout: `TerminationReason = "escalated"`, and one case takes the full 60 s. On Windows `procgroup.Signal` is `taskkill /T /F`; the tests run `sh` from Git Bash, whose children may not be in the tree taskkill sees. Whether a timed-out `cmd`/PowerShell child is ended in time is not measured yet. |
| `TestSearchFiles_NestedSubdirectory` | `search_files` returns `subdir\inner.txt`. A model given backslash paths may hand them back; decide whether tool output is slash-normalised. |
| `TestListSkills_HappyPath_EnumeratesBothRoots` | Only the workspace skill root is listed; the user root is not found (it is located through `HOME`, which Windows does not set). |
| `TestContextShow_TakesTheLimitFromTheRuntime` | The configured limit is not reconciled with the served one in `context show`; not yet read. |

## Open — the test assumes POSIX

| Tests | Assumption |
|---|---|
| `TestNormalize_RejectsAbsolutePath`, `TestAuthorize_ShellCwdIsPathShaped` | `/etc/passwd` and `/` are absolute. On Windows they are rooted on the current drive and `filepath.IsAbs` says no; `Normalize` re-roots them inside the workspace, which is safe. The policy now refuses a rooted path outright. |
| `TestShell_DefaultCwdIsTheWorkspace`, `TestStdioTransportStartsTheChildInTheGivenDirectory` | Compare Git Bash's `pwd` (`/c/Users/...`) with the Windows path. The directory is the right one. |
| `TestShell_ProcessGroupOwnership` | Reads PID and PGID from `ps -o`, which Git Bash's `ps` does not print. |
