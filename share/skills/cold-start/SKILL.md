# cold-start

You are about to begin work on this project. Before producing
your first output, orient yourself by reading the canonical
startup files. The harness has placed this instruction in your
context; the harness does NOT itself read any of these files
(it is an instruction injector, not a project-summarizer). If
a file does not exist, skip it; never invent content for a
missing file.

Read these in order; skip any that do not exist:

  1. README.md          project orientation; what is this
                        codebase, what is its current state,
                        who maintains it
  2. GOAL.md            the active Run contract (the planning
                        supervisor's mission for this Run);
                        what must be true at Run end
  3. DIRECTION.md       optional human-authored direction;
                        may not exist; do not invent
  4. git status         in-progress uncommitted work; the
                        working tree's current state
  5. source tree        the project's code layout; what
                        packages and entry points exist
  6. tests              what verification exists; the suite
                        contract's shape

When all six exist, produce a one-paragraph orientation
summary. When one or more are absent, note which and proceed
with what is present. Do not invent content for missing files.

Composition. This skill is composed into the model context at
the SCOPE §14 "skills" position — between the external
system / governance block (the `--system` / `--system-file`
content) and the user's task. The harness loads the SKILL.md
file and appends its body at that position; nothing in this
file is executed, evaluated, or otherwise interpreted by the
runtime. The cold-start skill is a markdown document, not a
program.

Source of truth. These file names live HERE, in this skill,
not in the harness runtime. A future revision of this SKILL.md
that adds new categories (for example `architecture/README.md`,
`spec/SPEC.md`, or `docs/SCOPE.md`) changes the skill, not the
harness. The harness's job is to load and inject this content;
the project's job is to decide which categories of project
state matter. If you are reading this because someone asked
you to add a startup category, edit this file and the binding
test `TestSkill_NoStartupNamesInRuntime` will continue to
pin the contract that the runtime never hardcodes the names.

Failure mode. If the harness reports "unknown skill" when
loading this file, the most likely cause is that the file is
not present at `share/skills/cold-start/SKILL.md` of the
checked-out source tree, or the operator has supplied a
`--skills-dir` that points at a directory which does not
contain a `cold-start/SKILL.md`. The loader is static; it
does not synthesize, generate, or fall back to a built-in
copy.
