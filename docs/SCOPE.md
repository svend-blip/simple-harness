# SCOPE — Simple Harness

> **OWNERSHIP: this document is the Human's.** It is the standing input the
> Planning Loop decomposes into Runs. The planning supervisor READS it and may
> PROPOSE changes to it as prose in a planning deliverable, but must not silently
> redefine this scope. Only the Human promotes scope changes into this document.
>
> Relationship to neighbouring planning artifacts:
>
> * `SCOPE.md` — WHAT Simple Harness is for and where its architectural
>   boundaries are. Changes rarely, by Human decision.
> * `RUNS-BACKLOG.md` — the run-by-run decomposition of this scope, maintained
>   by the Planning Supervisor.
> * Planning drafts — `goals/{ID}-GOAL-DRAFT.md`, produced by the Planning
>   Supervisor as ordinary planning deliverables.
> * `runs/NNN/GOAL.md` — the binding Run contract after Human review and
>   promotion.
>
> Run candidates in this document are planning seeds only. The Planning
> Supervisor may merge, split, reorder, defer, or reject them after inspecting
> the repository and relevant reference implementations.

---

## Mission

Build **Simple Harness** as a small, independent, terminal-first AI execution
harness for running one externally assigned AI role against one externally
managed model endpoint.

Simple Harness is inspired primarily by:

```text
Pi Coding Agent
Whip
```

but must not become a clone of either.

The project should deliberately extract the smallest useful execution
primitives from those projects and implement them as a deterministic,
observable, headless-friendly harness suitable both for standalone use and for
future integration through Harness Allocator.

The fundamental execution model is:

```text
one task
   ↓
one role
   ↓
one workspace
   ↓
one harness session
   ↓
one externally resolved model endpoint
   ↓
model/tool loop
   ↓
observable result
```

The primary architectural rule is:

> Simple Harness executes one role.
>
> It does not decide which role runs next.

Responsibility boundaries must remain:

```text
DPMtF
=
WHAT happens and in what sequence

Harness Allocator
=
WHICH execution frontend executes a role

Model Allocator
=
WHERE/HOW the model runs

Simple Harness
=
HOW one assigned AI role interacts with its model,
workspace, context and tools
```

Simple Harness must be independently usable.

It must not require:

```text
DPMtF
Harness Allocator
Model Allocator
```

to execute a task.

At the same time, its external process contract must be sufficiently stable
that Harness Allocator can later treat Simple Harness as a normal harness
backend.

The desired end state is not a feature-rich general agent platform.

The desired end state is:

> a small, boring, predictable, observable execution kernel for one AI role.

---

## Target project

Create a separate repository.

Expected target:

```text
/home/svend/simple-harness
```

or an equivalent standalone repository location selected at project creation.

The implementation must not be placed inside:

```text
/home/svend/DPMtF-WebUI
/home/svend/harness-allocator
/home/svend/model-allocator
```

Those projects may later consume Simple Harness through its public execution
contract.

No project may depend on importing Simple Harness internal modules as a shortcut
around that contract.

---

## Reference implementations

Two existing harnesses are primary architectural references.

### Pi Coding Agent

Study Pi primarily for:

```text
conceptual minimalism
small agent runtime
terminal coding-agent ergonomics
skills
sessions
tool integration
provider/model abstraction
extensibility without framework bloat
```

### Whip

Study Whip primarily for:

```text
small execution-loop design
streaming
terminal behavior
headless execution
process lifecycle
interrupt semantics
OpenAI-compatible model endpoints
tool execution
context observability
context diagnostics
future concurrency concepts
filesystem/path safety
child-process handling
single-purpose runtime design
```

The governing rule is:

```text
Pi
  → concepts worth understanding

Whip
  → runtime concepts worth understanding

Simple Harness
  → selectively implements only what this project requires
```

Do not copy features merely because Pi or Whip contains them.

The intended result should remain substantially smaller in scope than either
reference project.

---

## Current state

At scope adoption, Simple Harness does not yet exist as an implementation.

Relevant surrounding architecture already exists separately:

```text
DPMtF
Harness Allocator
Model Allocator
local OpenAI-compatible model runtimes
tmux-based execution
```

Harness Allocator is separately being designed to support multiple harnesses,
including external harnesses such as Pi and Whip.

Simple Harness is therefore not intended to replace those harnesses.

It is intended to become:

```text
a minimal standalone harness
        +
a deterministic local execution frontend
        +
a reference implementation of the desired Harness Adapter contract
```

The Planning Supervisor must begin from the actual current Pi and Whip
implementations rather than from assumptions encoded in this scope.

---

# In scope

## 1. Reference reconnaissance before implementation

Before substantial production code is written, inspect the current upstream
architectures of:

```text
Pi Coding Agent
Whip
```

Determine:

```text
what their actual execution loops do
how they represent messages
how they expose tools
how they stream model output
how they manage sessions
how they handle interrupts
how they handle child processes
how they load reusable instructions/skills
how they handle context
how they interact with OpenAI-compatible endpoints
which capabilities are core primitives
which capabilities are product-specific features
```

The reconnaissance must explicitly classify relevant ideas as:

```text
ADOPT
ADAPT
REJECT
DEFER
```

where useful.

Do not begin by reproducing either source tree.

The first implementation architecture must be derived from the needs of Simple
Harness.

---

## 2. Implementation-language ADR

Do not assume Python merely because surrounding DPMtF infrastructure uses
Python.

Before implementation, produce an Architecture Decision Record comparing at
minimum:

```text
Python
Go
```

The decision should consider the actual Pi and Whip implementations and weight
at least:

```text
process reliability
streaming
signal handling
headless execution
deployment simplicity
concurrency
child-process ownership
testability
maintainability
implementation complexity
dependency footprint
long-term independence from DPMtF
```

Python advantages worth evaluating include:

```text
development speed
simple API integration
mature libraries
pytest
easy experimentation
existing local familiarity
```

Go advantages worth evaluating include:

```text
single-binary style deployment
strong process/runtime boundary
goroutines/channels
streaming
signal handling
concurrency
low runtime dependency
predictable deployment
future parallel tool execution
```

The language decision must be explicit and recorded.

It must not be made solely for ecosystem consistency.

---

## 3. Minimal agent execution loop

Implement a small, directly understandable model/tool loop.

Conceptually:

```text
task
 ↓
assemble context
 ↓
model request
 ↓
stream response
 ↓
tool calls?
 ├── no  → final response
 └── yes
       ↓
    validate
       ↓
    authorize
       ↓
    execute
       ↓
    record result
       ↓
    append result
       ↓
    model request
```

Avoid hidden recursion and implicit orchestration.

The core loop should remain simple enough that its behavior can be audited from
the implementation.

Provide configurable safety limits such as:

```text
maximum turns
maximum tool calls
maximum execution time
```

Exceeding a limit must produce an explicit observable result.

---

## 4. Interactive terminal mode

Provide a terminal-first interactive mode.

Example target usage:

```bash
simple-harness \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace /home/user/project \
  --permission workspace_write
```

Interactive mode must support:

```text
streaming model output
large prompts
multi-line pasted input
visible status
visible active model
visible endpoint
visible workspace
visible permission mode
visible session identity
clean interruption
clean exit
```

The interface should remain lightweight.

Do not build a large full-screen TUI unless later evidence demonstrates a need.

---

## 5. Headless/non-interactive execution

Headless execution is a primary requirement.

Example target:

```bash
simple-harness run \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace /home/user/project \
  --permission workspace_write \
  --prompt-file task.md
```

stdin-based task input may also be supported:

```bash
cat task.md | simple-harness run ...
```

Headless mode must:

```text
require no browser
require no interactive confirmation unless explicitly configured
work correctly under tmux
produce deterministic exit codes
emit machine-readable execution events
respond correctly to SIGINT/SIGTERM
flush final execution state before exit where practical
```

An external controller must not need terminal scraping to determine execution
state.

---

## 6. OpenAI-compatible model interface

Initial model support should use an explicit OpenAI-compatible endpoint.

Primary interface:

```text
/v1/chat/completions
```

Support where available:

```text
streaming
tool/function calling
configurable base URL
configurable model name
optional API key
temperature
maximum output tokens
request timeout
```

Example configuration:

```yaml
model:
  provider: openai_compatible
  base_url: http://127.0.0.1:8080/v1
  model: qwen
  api_key: null
  temperature: 0.2
  max_output_tokens: 8192
```

Do not make assumptions about the runtime behind that endpoint.

The same harness should be capable of operating against compatible endpoints
provided by environments such as:

```text
llama.cpp
FreeToken
SGLang
vLLM
Ollama compatibility mode
local gateways
remote OpenAI-compatible services
cloud OpenAI-compatible providers
```

provided the required protocol features are actually compatible.

Provider-specific expansion is not required merely to increase provider count.

---

## 7. Model lifecycle separation

Simple Harness must not:

```text
select GPUs
allocate VRAM
load models
unload models
choose runtime profiles
select local versus cloud execution
start Model Allocator
implement Model Allocator policy
```

It consumes a resolved model endpoint.

Conceptually:

```text
external resolver/manual configuration
             ↓
{
  base_url,
  model,
  credentials/options
}
             ↓
       Simple Harness
```

This must work equally when the endpoint is supplied manually.

Model Allocator integration belongs outside the harness.

---

## 8. Core deterministic tool set

Provide a deliberately small initial tool set.

Expected V1 candidates:

```text
read_file
write_file
apply_patch
list_directory
search_files
grep
shell
```

Exact naming may be refined during planning.

Each tool must have:

```text
explicit schema
input validation
permission enforcement
structured result
observable start/completion
defined failure behavior
```

Do not introduce a large generalized plugin framework for basic tools.

---

## 9. File reading and repository inspection

`read_file` must support appropriate combinations of:

```text
workspace-relative paths
safe normalized absolute paths where permitted
optional line ranges
reasonable file-size limits
binary-file rejection/detection
structured failure
```

`list_directory`, `search_files`, and `grep` should permit efficient repository
inspection without forcing the model to consume entire source trees.

Existing deterministic native tools such as `rg` may be used where appropriate.

---

## 10. Deterministic file modification

Support direct file writing where appropriate, but prefer deterministic patching
for source modifications.

`apply_patch` should use a clearly defined patch representation such as unified
diff or another deterministic equivalent.

Patch behavior must:

```text
verify target
respect workspace boundary
respect permission mode
detect failed hunks
reject ambiguous application
return structured evidence
```

A failed patch must not be silently approximated.

The model may retry with a corrected patch.

The harness itself must not guess what the model intended to modify.

---

## 11. Shell execution

Provide controlled shell execution.

Example conceptual request:

```json
{
  "command": "pytest -q",
  "timeout_seconds": 120
}
```

Structured results should include at least:

```text
exit code
stdout
stderr
duration
termination reason where relevant
```

Support:

```text
working directory
timeout
cancellation
output-size protection
permission enforcement
child-process cleanup
```

Shell execution must not provide a path around the permission model.

---

## 12. Explicit permission modes

Implement normalized permission modes:

```text
READ_ONLY
WORKSPACE_WRITE
FULL_ACCESS
```

Case/style in the CLI may vary, but the semantic modes must remain explicit.

### READ_ONLY

Permit appropriate repository inspection.

Reject modifications.

### WORKSPACE_WRITE

Permit:

```text
reading workspace
writing workspace
patching workspace
running normal development/test commands
```

Reject unauthorized writes outside the workspace.

This should be the normal coding-agent mode.

### FULL_ACCESS

Permit broader operations subject to OS permissions.

Must be explicitly selected.

Simple Harness must never silently escalate permissions.

---

## 13. Permission enforcement at the execution boundary

Permission restrictions must be enforced by deterministic harness code.

They must not rely on an LLM obeying prose instructions.

Every relevant tool request must pass through:

```text
schema validation
        ↓
path normalization
        ↓
permission policy
        ↓
execution
```

The active effective permission level must always be externally observable.

---

## 14. External system/governance instructions

Simple Harness must accept externally supplied instructions.

Examples:

```text
system prompt
governance file
role instructions
task-specific execution instructions
```

Possible CLI interfaces include:

```bash
--system "..."
--system-file governance.md
```

Do not hard-code DPMtF role semantics into Simple Harness.

The harness only understands that externally provided instruction material is
part of the model context.

Instruction ordering must be deterministic.

A likely ordering is:

```text
minimal harness system instructions
              ↓
external system/governance
              ↓
loaded skills
              ↓
task
```

The resolved composition must be inspectable through context diagnostics.

---

## 15. Skills

Implement a lightweight reusable skill mechanism inspired by Pi and Whip.

Possible global location:

```text
~/.simple-harness/skills/
```

Possible project-local location:

```text
.simple-harness/skills/
```

A minimal skill may consist of:

```text
<skill-name>/SKILL.md
```

Support invocation concepts such as:

```text
/skill cold-start
```

and:

```bash
--skill cold-start
```

V1 skills should primarily inject reusable instructions/context.

Do not turn the skills mechanism into a general plugin marketplace or agent
framework.

---

## 16. Cold-start reference skill

Provide or validate a `cold-start` reference skill.

It may instruct the assigned AI role to inspect relevant project state such as:

```text
README
GOAL.md
DIRECTION.md
git status
source tree
tests
other project-specific startup material
```

These names must not be hard-coded into the harness runtime.

That behavior belongs to the skill.

The skill should demonstrate that startup behavior can evolve without changing
the harness core.

---

## 17. Sessions

Every execution must have a stable session identity.

Sessions should support enough persistence for:

```text
inspection
debugging
external correlation
conversation continuation where supported
interrupted-run diagnosis
```

A possible representation is:

```text
sessions/<session-id>/
    session.json
    messages.jsonl
    events.jsonl
```

The exact location and format may change during planning.

Session persistence is execution history.

It is not semantic long-term memory.

Do not build an autonomous memory system as part of this scope.

---

## 18. Context management

The harness must explicitly track the context sent to the model, including as
applicable:

```text
minimal harness instructions
external governance/system instructions
skills
task/user messages
assistant messages
tool schemas
tool calls
tool results
```

The implementation should know the configured model context limit when supplied.

Context management must fail predictably if the request cannot fit rather than
silently corrupting the conversation.

Sophisticated automatic context summarization is not required for the initial
implementation.

The architecture should not prevent later explicit compaction.

---

## 19. Context observability

Take explicit inspiration from Whip's context observability.

Provide functionality equivalent to:

```text
/context
/context-doctor
```

Exact command names may differ.

The harness should be able to report approximate or exact consumption such as:

```text
Harness system            1,200 tokens
Governance                4,100 tokens
Skill: cold-start           900 tokens
Task                      1,500 tokens
Conversation             12,600 tokens
Tool schemas              1,900 tokens
Tool results              4,200 tokens
-----------------------------------------
Total                    26,400 tokens
Context limit           131,072 tokens
```

Where exact tokenization is not available, clearly identified estimates are
acceptable.

The mechanism must make context overhead visible rather than hidden.

---

## 20. Context diagnostics

Provide diagnostics capable of identifying major context contributors such as:

```text
large governance files
large skills
duplicate instructions
large file reads
large tool results
conversation history
tool-schema overhead
```

V1 diagnostics should primarily report.

Do not automatically discard context in a way that may change execution meaning
without explicit policy.

---

## 21. Structured event protocol

Machine-readable progress is a core requirement.

Provide a versioned event protocol, preferably:

```text
JSONL
```

Conceptual examples:

```json
{"protocol_version":"1","event":"started","session_id":"abc123"}
{"protocol_version":"1","event":"status","status":"WAITING_FOR_MODEL"}
{"protocol_version":"1","event":"model_request","turn":1}
{"protocol_version":"1","event":"assistant_stream","text":"Inspecting..."}
{"protocol_version":"1","event":"tool_call","call_id":"t001","tool":"read_file"}
{"protocol_version":"1","event":"tool_result","call_id":"t001","status":"ok"}
{"protocol_version":"1","event":"status","status":"COMPLETED"}
{"protocol_version":"1","event":"completed","exit_code":0}
```

The final schema may differ.

The invariant is:

> Harness Allocator or another external controller must not need to scrape
> decorative terminal output to understand Simple Harness execution.

---

## 22. Human output and machine output separation

Interactive terminal presentation and machine control output must be separable.

Possible interface:

```text
--output terminal
--output jsonl
```

or separate streams/file descriptors.

The Planning Supervisor should choose the smallest reliable design.

Human-readable UI formatting must not become part of the machine-control
contract.

---

## 23. Observable status model

Expose real runtime states.

Candidate states:

```text
STARTING
READY
WAITING_FOR_MODEL
STREAMING
READING
SEARCHING
WRITING
PATCHING
RUNNING_TOOL
INTERRUPTING
COMPLETED
FAILED
CLEANUP
INTERRUPTED
```

Do not present hidden model reasoning or inferred chain-of-thought as status.

For example:

```text
WAITING_FOR_MODEL
```

means the harness currently has an active model request.

Statuses must correspond to actual execution state.

---

## 24. Large-paste and multiline input

Interactive mode must handle large pasted tasks robustly.

A multiline paste must not accidentally execute fragments as:

```text
shell commands
slash commands
separate accidental prompts
```

Natural terminal paste is preferred.

An explicit mode such as:

```text
/paste
```

may be introduced only if it materially improves reliable handling.

Do not impose arbitrary short prompt limits.

---

## 25. Ctrl+C and interruption semantics

Interrupt semantics are part of the public runtime contract.

Interactive target behavior:

```text
model generating
    +
Ctrl+C
    ↓
cancel active model request
preserve session
return to prompt
```

For cancellable tools:

```text
tool running
    +
Ctrl+C
    ↓
request tool cancellation
clean up owned subprocesses
preserve session where safe
```

Session termination must be distinguishable from task interruption.

Support an explicit exit mechanism such as:

```text
/exit
```

A documented repeated-Ctrl+C behavior may also be supported.

Do not allow a single task interrupt to accidentally destroy the parent tmux or
Harness Allocator execution chain.

---

## 26. SIGINT and SIGTERM for headless execution

Headless execution must handle process signals predictably.

Conceptually:

```text
SIGINT / SIGTERM
        ↓
mark interruption
        ↓
cancel active model/tool work
        ↓
terminate harness-owned child processes
        ↓
persist useful session state
        ↓
emit final interruption event
        ↓
flush event output
        ↓
exit using documented code
```

Signal behavior must be covered by automated tests.

---

## 27. Child-process ownership and cleanup

Shell commands and future tools may spawn subprocesses.

Simple Harness must explicitly own the lifecycle of processes it creates.

Investigate platform-appropriate mechanisms such as:

```text
process groups
signal propagation
timeout handling
controlled SIGTERM
controlled escalation where required
```

A terminated harness must not routinely leave behind:

```text
pytest processes
build commands
shell children
tool-owned background processes
```

Do not kill unrelated user processes.

Ownership must be traceable to the active harness execution/session.

---

## 28. Exit codes

Define stable process exit semantics.

An initial scheme may include:

```text
0  success
1  generic failure
2  configuration error
3  model/API failure
4  permission violation
5  tool failure
6  interrupted
```

Exact numeric values may change before V1 publication.

Once documented as the external V1 contract, they should remain backward
compatible unless deliberately versioned.

---

## 29. Configuration

Support a small predictable configuration hierarchy.

Possible locations:

```text
~/.simple-harness/config.yaml
.simple-harness/config.yaml
```

Suggested precedence:

```text
defaults
   ↓
user config
   ↓
project config
   ↓
environment
   ↓
CLI
```

The resolved configuration must be inspectable.

Example concept:

```bash
simple-harness config show
```

Do not create a complicated configuration language.

---

## 30. Secret handling

Credentials may be supplied using suitable mechanisms such as:

```text
environment variables
configuration
CLI where unavoidable
```

Secrets must not appear in:

```text
normal startup output
JSONL events
session logs
context diagnostics
HTTP diagnostic dumps
```

Sensitive headers must be redacted.

---

## 31. Deterministic validation of model tool calls

Every tool call generated by a model must be validated before execution.

Validate at minimum:

```text
known tool
schema
required parameters
argument types
paths
workspace boundary
permission level
timeout limits
other tool-specific constraints
```

Malformed LLM output is untrusted input.

Do not pass it directly to the OS or filesystem.

---

## 32. Sequential V1 execution with concurrency-ready architecture

Whip's concurrency model is relevant architectural inspiration.

However, Simple Harness V1 may deliberately execute tool calls sequentially.

Priority is:

```text
correctness
   ↓
observability
   ↓
determinism
   ↓
reliability
   ↓
performance
```

The internal design should nevertheless avoid making future safe parallel tool
execution impossible.

Do not add concurrency solely for benchmark performance.

---

## 33. Future parallel read execution

The architecture may later permit independent operations such as:

```text
read file A
read file B
grep X
grep Y
```

to execute concurrently where semantics remain deterministic.

This is an architectural extension point.

It is not a requirement that V1 implement parallel execution.

---

## 34. Future per-path locking

Study Whip's approach to concurrent filesystem safety.

If parallel tools are later introduced, Simple Harness should be capable of
protecting conflicting path operations.

Conceptually:

```text
read A + read A
    → parallel allowed

read A + write A
    → synchronize

write A + write A
    → serialize
```

Do not implement a complex locking subsystem before parallel execution exists.

Document the extension point instead.

---

## 35. Standalone operation

The following must work without surrounding DPMtF infrastructure:

```text
clone/build Simple Harness
        ↓
start or identify OpenAI-compatible model endpoint
        ↓
run Simple Harness
        ↓
perform coding/review task
```

No Model Allocator dependency.

No Harness Allocator dependency.

No DPMtF dependency.

This is an acceptance requirement, not merely a design preference.

---

## 36. Harness Allocator readiness

Although Harness Allocator integration itself belongs outside this repository,
Simple Harness must expose enough public lifecycle semantics for a future adapter
to perform conceptual operations such as:

```text
probe
prepare
start
send
status
interrupt
collect
resume
cleanup
```

Not every lifecycle operation must require a bespoke API.

A CLI/process/event implementation may satisfy several of them.

The public boundary should include enough of:

```text
CLI invocation
session identity
structured events
signals
exit codes
process lifecycle
resolved status
```

that Harness Allocator does not need Simple Harness-specific terminal scraping.

---

## 37. Reference implementation for the Harness Adapter contract

Simple Harness should become the cleanest reference implementation against
which Harness Allocator's normalized harness lifecycle can be validated.

The intended relationship is:

```text
Harness Allocator
      |
      +-- Simple Harness adapter
      +-- Pi adapter
      +-- Whip adapter
      +-- Codex adapter
      +-- Claude Code adapter
      +-- OpenCode adapter
      +-- future adapters
```

Simple Harness must not receive private integration shortcuts.

If the generic Harness Adapter contract cannot represent Simple Harness
cleanly, improve the contract rather than introducing hidden coupling.

Likewise, Simple Harness should not contain Harness Allocator-specific workflow
logic.

---

## 38. Comparative validation against Pi and Whip

Use Pi and Whip as practical baselines for small deterministic coding tasks.

Run representative equivalent tasks through:

```text
Pi
Whip
Simple Harness
```

Compare where measurable:

```text
startup behavior
initial context overhead
tool sequence
number of turns
streaming behavior
context observability
interrupt behavior
session behavior
process cleanup
final repository state
test result
headless usability
```

The purpose is not to win a synthetic benchmark.

The purpose is to expose unnecessary Simple Harness complexity and verify the
intended minimal runtime.

---

## 39. Automated tests

Testing must begin with the architecture rather than being added only after
implementation.

Minimum coverage should include:

```text
configuration precedence
configuration failure
workspace normalization
workspace escape prevention
READ_ONLY enforcement
WORKSPACE_WRITE enforcement
FULL_ACCESS behavior
read_file
write_file
apply_patch success
apply_patch failure
directory listing
file search
grep
shell success
shell non-zero exit
shell timeout
tool cancellation
child-process cleanup
tool schema validation
unknown tool rejection
model API failure
stream parsing
tool-call loop
maximum-turn safeguard
maximum-tool-call safeguard
session persistence
session correlation
event schema
event ordering
exit codes
SIGINT
SIGTERM
secret redaction
skill loading
context composition
context diagnostics
large prompt handling
```

Use mocked model responses for deterministic unit tests.

Add optional integration tests against a real OpenAI-compatible local endpoint.

---

## 40. End-to-end coding acceptance test

Create a small repository fixture such as:

```text
example-project/
├── calculator.py
└── test_calculator.py
```

with a known failing behavior.

Execute:

```bash
simple-harness run \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace ./example-project \
  --permission workspace_write \
  --prompt "Find and fix the defect. Run the tests afterward." \
  --output jsonl
```

The observable execution should include equivalent states to:

```text
started
↓
model request
↓
repository inspection
↓
test execution
↓
defect identification
↓
file patch
↓
test execution
↓
PASS
↓
final response
↓
completed
```

The caller must be able to determine success using structured execution state.

---

## 41. Read-only reviewer acceptance test

A second acceptance scenario should prove Simple Harness is usable as a review
frontend.

Conceptually:

```bash
simple-harness run \
  --base-url http://127.0.0.1:8080/v1 \
  --model reviewer \
  --workspace ./example-project \
  --permission read_only \
  --prompt "Review the implementation and run appropriate read-only checks."
```

The harness must prove that:

```text
source inspection works
tests/checks may run where policy permits
repository mutation is rejected
final review is returned
```

This helps demonstrate that Simple Harness is role-neutral.

---

## 42. Backward-compatible public contract

Once a V1 external contract is published, changes to:

```text
CLI invocation
event schema
exit codes
signal semantics
session identity
permission semantics
```

must be treated as compatibility-sensitive.

Future extensions should prefer additive evolution.

Introduce protocol/version changes deliberately where incompatible changes are
unavoidable.

---

## 43. MCP client — external tool provider

Provide an MCP client that surfaces tools from configuration-pinned
MCP servers through the existing tool registry.

An MCP-provided tool is indistinguishable from a builtin at the
execution boundary. It passes through the same pipeline:

```text
explicit schema (taken from the server's tool listing)
input validation (§31)
permission enforcement (§12-§13)
structured result
observable start/completion (§21 event protocol)
defined failure behavior
```

Servers are declared in configuration only:

```text
server name (stable identifier)
transport: stdio | http
endpoint or command
permission mode the server's tools map into
optional tool allowlist (subset of what the server offers)
```

No server is reachable that is not declared. No tool is callable that
the allowlist excludes. Secret material in server configuration
follows §30.

The tool listing is fetched once at session start and is immutable for
the session. The resolved set (servers, tools, schemas, permission
mapping) must be inspectable through context diagnostics (§20).

A server that is declared but unreachable at session start is a
structured startup error, not a silent omission.

Transport failures during a tool call are structured tool failures
(the model sees them), never harness crashes.

---

## 44. mcp-light reference integration

The first-party DPMtF governance server `mcp-light` (local HTTP) is
the reference MCP server, in the same role Pi and Whip hold for the
harness itself: the integration is proven against it, not modeled
on it.

Acceptance is a live end-to-end test in the shape of §40:

```text
harness configured with mcp-light pinned, read-only tool subset
model asks a question only an mcp-light tool can answer
events show tool_call -> tool_result against the MCP server
assertion from events + exit code, zero terminal scraping
```

Do not hard-code mcp-light semantics into the harness (§14's rule:
the harness does not understand DPMtF). mcp-light is configuration,
not code.

---

## 45. Model-invoked skills

The skill mechanism (§15) is human-invoked in V1: `--skill` at launch,
`/skill` at the prompt. Once tool dispatch (§3 loop shape) has landed,
expose skill loading to the model as a builtin tool.

The tool:

```text
lists the skills discoverable under §15's locations
loads a named skill's instruction material into the next
  model request's context
is permission-gated like any other tool
emits observable start/completion events (§21)
```

No new trust surface: skills remain local files from the §15
locations only. The tool never fetches skill material from anywhere
else.

A loaded skill must be visible in context diagnostics (§20), with the
same deterministic ordering guarantee as §14.

Do not let the model write or modify skills through this tool;
loading is read-only.

---

# Out of scope

The following are outside this scope unless the Human revises `SCOPE.md`.

## 1. Multi-agent orchestration

Do not implement:

```text
Supervisor → Implementer → Reviewer
planner loops
role routing
workflow graphs
autonomous role selection
```

inside Simple Harness.

That belongs above the harness.

---

## 2. Background subagents

Whip's subagent concepts may be studied, but background subagents are not part
of Simple Harness V1.

Do not introduce an internal second layer of orchestration.

A future reconsideration requires an explicit architectural decision.

---

## 3. DPMtF workflow logic

Simple Harness must not know:

```text
which DPMtF step comes next
which role should receive a handoff
whether a GOAL is complete
whether a Run should close
whether a verdict should trigger escalation
```

These are DPMtF concerns.

---

## 4. Harness selection

Simple Harness must not decide whether:

```text
Pi
Whip
Codex
Claude Code
OpenCode
Simple Harness
```

should execute a task.

Harness selection belongs to Harness Allocator/DPMtF policy.

---

## 5. Model allocation

Simple Harness must not become Model Allocator.

Do not implement generic:

```text
GPU allocation
model lifecycle
VRAM management
runtime scheduling
model alias policy
local/cloud selection
automatic runtime selection
```

---

## 6. Automatic model selection

Do not add AI-based or heuristic model recommendation/routing in V1.

The harness receives the model configuration it should use.

---

## 7. Provider marketplace/catalog

Do not reproduce broad provider catalogs merely because a reference harness
contains them.

Add provider-specific behavior only when required by an approved Run.

The initial generic OpenAI-compatible boundary is sufficient.

---

## 8. Browser automation and computer use

Do not implement:

```text
browser automation
desktop control
Playwright agent
computer-use agent
GUI automation
```

as part of the Simple Harness core.

---

## 9. Built-in web research

Do not make Simple Harness a research browser.

Web/search capabilities may later be supplied through explicit tools or trusted
MCP when separately approved.

---

## 10. Semantic long-term memory

Do not implement:

```text
vector databases
embedding memory
autonomous memory retrieval
cross-project agent memory
```

Session persistence is in scope.

Semantic memory is not.

---

## 11. Unrestricted MCP

Do not implement dynamic MCP server discovery or unrestricted
third-party server loading.

MCP servers are configuration-pinned; see In scope §43.

Do not implement server-initiated model requests (sampling) or
server-initiated file access (roots) in the first MCP revision.

---

## 12. Plugin marketplace

Do not implement a plugin store or unrestricted third-party tool-loading
framework.

The initial tool and skill mechanisms must remain inspectable.

---

## 13. Complex full-screen TUI

Do not build a large terminal UI framework unless later evidence shows that the
simple terminal interface is insufficient.

Functionality and process semantics take priority over presentation.

---

## 14. Mandatory parallel tool execution

Concurrency architecture may be studied.

Parallel tool execution is not required for the first usable harness.

Do not trade deterministic behavior for premature performance.

---

## 15. Workspace-level multi-agent concurrency policy

Deciding whether multiple roles may concurrently operate on a repository is not
a Simple Harness workflow responsibility.

Harness Allocator may later enforce:

```text
parallel READ_ONLY sessions
exclusive WORKSPACE_WRITE lease
```

Simple Harness only enforces the permission boundary of its own execution.

---

## 16. Commit/push orchestration policy

Simple Harness may execute Git commands if explicitly permitted by the active
permission/governance configuration.

It must not independently decide:

```text
when a commit is authorized
when push is authorized
who may approve it
which DPMtF lifecycle point permits it
```

Those are external governance concerns.

---

## 17. Replacing Pi or Whip

Simple Harness is not a project to obsolete either reference harness.

Pi and Whip should remain valid Harness Allocator backends where useful.

Simple Harness exists to provide a smaller deterministic baseline.

---

# Standing constraints

* Simple Harness is a standalone repository.

* Existing DPMtF, Harness Allocator and Model Allocator repositories must remain
  independently operable.

* Simple Harness must remain usable without any of those three systems.

* Simple Harness executes one externally assigned role at a time.

* Workflow orchestration must remain outside Simple Harness.

* Model lifecycle must remain outside Simple Harness.

* Harness selection must remain outside Simple Harness.

* Permission enforcement must be deterministic code, not model instruction.

* No silent permission escalation is permitted.

* Machine-readable execution state must not depend on scraping human terminal
  output.

* Model-generated tool calls are untrusted input and must be validated.

* Workspace boundaries must be enforced deterministically.

* Signal and child-process behavior are part of the runtime contract and must
  be tested.

* Context consumption must be observable.

* Context diagnostics must not imply more tokenizer precision than the harness
  actually possesses.

* V1 should prefer sequential correctness over premature concurrency.

* Architecture should not unnecessarily prevent future safe concurrency.

* Skills are reusable instruction packages, not an internal orchestration
  framework.

* Session persistence is execution history, not semantic memory.

* Pi and Whip are architectural references, not source trees to clone.

* The implementation language must be selected through an explicit ADR.

* New abstractions must solve measured implementation needs rather than
  anticipated framework ambitions.

* Planning must prefer small, independently testable Runs over one large
  implementation Run.

* The public V1 CLI/event/signal/exit-code contract is compatibility-sensitive
  once published.

---

# Run candidates — seed for the backlog

These are planning seeds, not approved Runs.

The Planning Supervisor must inspect:

```text
current Pi implementation
current Whip implementation
available local model endpoints
expected Harness Allocator contract
relevant process/signal constraints
existing Simple Harness repository if already initialized
```

before producing the final decomposition.

The Planning Supervisor may:

```text
merge
split
reorder
defer
reject
```

these candidates.

The first Planning Supervisor wake-up should produce planning artifacts, not a
large production implementation.

---

## Candidate 001 — Pi/Whip Reconnaissance and Architecture ADR

Inspect Pi and Whip.

Document:

```text
agent loops
provider/model interfaces
tool contracts
streaming architecture
session handling
skills
context handling
context diagnostics
interrupt semantics
child-process behavior
headless interfaces
concurrency architecture
```

Classify relevant concepts:

```text
ADOPT
ADAPT
REJECT
DEFER
```

Produce the Python-versus-Go ADR.

Produce the minimal Simple Harness architecture.

Do not implement later feature layers prematurely.

---

## Candidate 002 — Repository Bootstrap and Minimal Model Loop

Create the standalone repository and initial build/test structure.

Implement:

```text
CLI
configuration core
OpenAI-compatible client
streaming
minimal conversation state
basic interactive execution
```

Acceptance:

```text
Simple Harness can converse with a local OpenAI-compatible model endpoint.
```

No tools required beyond what is necessary for the vertical slice.

---

## Candidate 003 — Core Read/Search Tools

Implement deterministic repository inspection:

```text
read_file
list_directory
search_files
grep
```

Add:

```text
workspace normalization
tool schemas
tool validation
structured results
```

Acceptance:

```text
the model can independently inspect and understand a small repository without
receiving the complete repository in its initial prompt
```

---

## Candidate 004 — Permissions and Write/Patch Tools

Implement:

```text
READ_ONLY
WORKSPACE_WRITE
FULL_ACCESS
```

and:

```text
write_file
apply_patch
```

Prove:

```text
READ_ONLY cannot mutate
WORKSPACE_WRITE cannot escape workspace
FULL_ACCESS is only enabled explicitly
failed patches do not silently approximate
```

---

## Candidate 005 — Shell Execution and Process Ownership

Implement:

```text
shell
timeouts
structured stdout/stderr
exit status
cancellation
child-process ownership
child-process cleanup
```

Exercise realistic commands such as:

```text
pytest
project test commands
short build commands
```

Prove that interruption does not routinely leave orphaned harness-owned
processes.

---

## Candidate 006 — Headless Contract and Structured Events

Implement/stabilize:

```text
non-interactive run mode
prompt-file
stdin input where appropriate
system-file
JSONL events
status model
exit codes
```

Acceptance:

```text
an external script can start Simple Harness, observe meaningful progress and
determine completion without terminal scraping
```

This is a key vertical slice for future Harness Allocator integration.

---

## Candidate 007 — Signal and Interrupt Semantics

Implement and test:

```text
interactive Ctrl+C
SIGINT
SIGTERM
task cancellation
tool cancellation
session preservation
explicit harness termination
event flushing
cleanup
```

Prove task interruption and harness termination are distinct operations.

Verify behavior under tmux.

---

## Candidate 008 — Sessions

Implement:

```text
session IDs
conversation persistence
event persistence
session inspection
resume where safely supportable
external request/session correlation
```

Do not add semantic memory.

Acceptance:

```text
completed and interrupted executions can be inspected and correlated
deterministically
```

---

## Candidate 009 — Skills and Cold Start

Implement lightweight:

```text
global skills
workspace skills
/skill
--skill
```

Provide `cold-start` as the reference skill.

Prove that reusable startup behavior changes without modifying harness runtime
code.

---

## Candidate 010 — Context Observability

Implement context accounting and diagnostics.

Provide functionality equivalent to:

```text
/context
/context-doctor
```

Expose consumption categories such as:

```text
harness system
external governance
skills
task
conversation
tool schemas
tool results
```

Prove that large context contributors are visible.

Do not implement aggressive automatic compaction merely to satisfy this Run.

---

## Candidate 011 — End-to-End Coding Agent Vertical Slice

Exercise Simple Harness against a deliberately small repository with a failing
test.

Required behavior:

```text
inspect repository
run tests
identify defect
patch source
rerun tests
report result
```

Verify:

```text
workspace state
structured event trail
permission enforcement
final tests
session record
exit status
```

This Run should establish whether the harness is genuinely usable rather than
merely API-complete.

---

## Candidate 012 — Read-Only Reviewer Vertical Slice

Run an equivalent repository task using:

```text
READ_ONLY
```

Prove:

```text
inspection works
review/check commands work where allowed
mutation attempts are rejected
review result is returned
```

This demonstrates that Simple Harness is not hardcoded to an Implementer role.

---

## Candidate 013 — Pi/Whip Comparative Validation

Execute equivalent bounded tasks using:

```text
Pi
Whip
Simple Harness
```

Measure where practical:

```text
startup
context overhead
tool sequence
model turns
streaming
context visibility
interrupt behavior
session behavior
process cleanup
test outcome
headless usability
```

Use findings to remove unnecessary complexity from Simple Harness.

Do not expand scope merely to match every Pi/Whip feature.

---

## Candidate 014 — Harness Allocator Contract Readiness

Without implementing Harness Allocator itself, verify that Simple Harness
exposes sufficient public behavior for an adapter to implement conceptual:

```text
probe
prepare
start
send
status
interrupt
collect
resume
cleanup
```

Stabilize:

```text
CLI contract
JSONL protocol
session identity
status semantics
signals
exit codes
process ownership
```

Acceptance:

> no Harness Allocator implementation should need to inspect Simple Harness
> private modules or scrape decorative terminal text.

---

## Candidate 015 — Concurrency Architecture Review

Only after the sequential execution kernel is stable, revisit Whip's concurrency
architecture.

Determine whether to:

```text
ADOPT
ADAPT
DEFER
REJECT
```

concepts such as:

```text
parallel independent reads
parallel searches
per-path locking
tool-call scheduling
```

This Run may produce architecture and tests without enabling parallel execution.

Concurrency must not reduce correctness, observability or deterministic
filesystem behavior.

---

# Architectural acceptance criteria

The overall scope is satisfied when:

1. Simple Harness exists as an independent repository.

2. Pi and Whip have been studied as architectural references rather than copied
   wholesale.

3. The implementation language was selected through a documented Python-versus-
   Go ADR.

4. Simple Harness executes one externally assigned AI role.

5. It works interactively from a Linux terminal.

6. It works correctly under tmux.

7. It has a fully usable headless/non-interactive mode.

8. It can use an explicitly configured OpenAI-compatible model endpoint.

9. It does not own model selection or model lifecycle.

10. It implements a small understandable model/tool loop.

11. It provides deterministic read/search/write/patch/shell tools.

12. Model-produced tool calls are schema-validated.

13. Workspace boundaries are enforced.

14. `READ_ONLY`, `WORKSPACE_WRITE`, and `FULL_ACCESS` have deterministic,
    tested semantics.

15. Permission mode is externally visible.

16. External system/governance instructions can be injected without hardcoding
    DPMtF concepts.

17. Reusable skills are supported.

18. A cold-start skill demonstrates the skill mechanism.

19. Sessions have stable identities and inspectable execution history.

20. Context composition and approximate/exact consumption are observable.

21. Context diagnostics can identify major context consumers.

22. Streaming output works.

23. Machine-readable execution events are available.

24. Human presentation is separate from the machine-control contract.

25. Exit codes have documented meaning.

26. Ctrl+C/SIGINT semantics are deterministic.

27. SIGTERM semantics are deterministic.

28. Task interruption is distinguishable from harness termination.

29. Harness-owned subprocesses are cleaned up predictably.

30. A small repository can be autonomously inspected, modified and tested.

31. The same harness can operate in a read-only reviewer scenario.

32. Simple Harness can run without DPMtF.

33. Simple Harness can run without Harness Allocator.

34. Simple Harness can run without Model Allocator.

35. Harness Allocator can later integrate it through public process/event
    semantics rather than private implementation coupling.

36. The architecture does not require parallel tool execution for correctness.

37. The architecture does not unnecessarily prevent future safe parallel tool
    execution.

38. Simple Harness remains substantially smaller in conceptual scope than Pi or
    Whip.

39. It has not evolved into a workflow orchestrator.

40. It provides a credible reference implementation for the common Harness
    Adapter lifecycle.

---

# Final direction

> Pi and Whip are references, not templates.
>
> Pi should inform minimalism, skills, sessions and coding-agent ergonomics.
>
> Whip should inform the execution loop, streaming, process behavior, context
> observability, interruption semantics and future-safe concurrency.
>
> DPMtF remains the orchestrator.
>
> Harness Allocator remains the harness control plane.
>
> Model Allocator remains the model/runtime control plane.
>
> Simple Harness executes one role well.

The Planning Supervisor should therefore optimize for:

```text
small
understandable
deterministic
observable
testable
replaceable
terminal-first
headless-first
```

and resist turning Simple Harness into a general-purpose agent framework.


---

# Amendment log

## 2026-08-29 — MCP client + model-invoked skills (V2 wave)

Applied from `SCOPE-AMENDMENT-MCP-DRAFT.md` on the Human's order,
recorded in the planning-supervisor session 2026-08-29 ("medtag
SCOPE-AMENDMENT-MCP-DRAFT.md ... få det hele afviklet i kæden",
followed by the Human pointing the supervisor at the amendment file
for application). Changes: Out-of-scope §11 replaced (was "General
MCP framework in V1"); In-scope §§43-45 added. All V1 runs (001-015,
017, 018) were closed and the V1 SCOPE validation complete before
application; no live gate was reading this document. Decomposed as
runs 019 (MCP client), 020 (mcp-light integration), 021
(model-invoked skills).
