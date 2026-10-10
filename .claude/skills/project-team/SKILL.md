---
name: project-team
description: >
  Run the spoved agent team inside a Claude project, where the project's main
  session can't create sessions and opens threads instead. One thread per
  ticket: its lead runs the coder, security reviewer and tester as in-session
  subagents. The main session is the supervisor and talks to the user. Use when
  asked to "run the team in a project", "start a ticket thread", or to replace
  or resume a project thread. Outside a project, use the agent-team skill.
argument-hint: "[supervisor|ops|ticket]"
---

# Project team

Not in use since 10 Oct (user decision): the agent-team skill runs all work, and
only its supervisor merges into develop. Kept for reference; ask the user before
using it again.

Same roles, rules and gates as `.claude/skills/agent-team/SKILL.md` (read it for
the "Rules for everyone" block, the coder, security and tester briefs, and
Lessons). Only the plumbing changes: a project has one main session plus
threads, and a thread can address only its parent. So relays are expensive, and
each ticket runs in one thread that does all its own work.

| agent-team | project-team |
|---|---|
| Supervisor = the user's session | Supervisor = the project's main session |
| `create_session` per lead or worker | Open one thread per ticket; the brief is its first message |
| OL session | One long-lived `[OL]` thread (or the supervisor does intake itself) |
| TL + coder + security + tester sessions | One `[T] #N` thread; coder, security reviewer and tester are `Agent` subagents inside it |
| `send_message(session_id)` | Main → thread: `send_message(to: "thread", thread_ts)`. Thread → main: `send_message(to: "parent")` |
| Session ids in briefs and prompts | Thread ids (`thread_ts`) |
| Tags `role:` / `supervisor:` | Thread title prefix: `[OL]`, `[T] #197` |
| `archive_session` a finished worker | Rename the thread `✅ DONE: …` and close or archive it |

GitHub is the shared record between threads: acceptance criteria and the
"OL AC check @<SHA> … VERDICT: FULLY DONE" verdict are issue and PR comments, so
no thread needs another thread's transcript.

## Verify once per project (probe thread)

Before the first ticket, open a thread with this message and record the answers
under "Project facts" below (in a docs PR):

```
Probe for the project-team skill. Answer each in one line, then stop:
1. Can you open another thread yourself? (try it only if it's free to undo)
2. Can you send_message to another thread directly, or only to "parent"?
3. Do send_later and the GitHub MCP tools (mcp__github__*) load here?
4. Which model are you running (get_session, session_context.model)?
5. Can you run the Agent tool, and can a subagent run Bash in the background?
```

If a thread *can* message other threads (2), the OL may talk to ticket threads
directly; otherwise everything goes through the supervisor in one line.
If threads can't run background Bash in subagents (5), the ticket thread runs
the test suite itself in the background.

## Project facts

Not yet verified. Fill in after the probe.

## Supervisor (main session)

1. Verify state as in agent-team "Supervisor setup" step 2 (open PRs, branches,
   migration letters).
2. Open the `[OL]` thread with the agent-team OL brief, with the TL lines replaced
   by: "Ticket threads report to the supervisor. Tell the supervisor the next
   ticket and its migration letter; the supervisor opens its thread."
3. For each ticket the OL names (at most 2 in flight; never two that share files,
   tables or a migration letter), open a `[T] #N` thread with the **ticket
   brief** below.
4. Arm a 4-hour check-in with `send_later` (agent-team check-in prompt, with
   thread ids). On each one, read each thread's status and context only;
   retire a thread past ~200k tokens with a 15-line hand-off into a new thread.
5. Relay only what each side needs, in one line: ticket → OL ("PR #N @SHA ready
   for AC check"), OL → ticket ("FULLY DONE @SHA" or the gaps), merges → user.
6. Tell the user only merges, blockers and what only they can do.

## Ticket brief (one thread per ticket)

```
You are the TICKET LEAD for #<N> in InfiniteSpoved/spoved, running in a Claude
project thread. Report only to the supervisor, with send_message(to: "parent"),
one line per milestone. Read CLAUDE.md, then .claude/skills/agent-team/SKILL.md
(TL brief, coder/security/tester briefs, Rules for everyone): follow the TL
pipeline, except that you don't create sessions. Run each worker as an Agent
subagent in this thread:
- Coder: the agent-team coder brief, with "<TL>" meaning "the caller"; phase 1
  returns the design to you, phase 2 runs after you approve it.
- Security reviewer (only when the TL brief's step 3 applies): the security
  brief, design round then code round. Its findings are must-fix.
- Tester: the tester brief. The full suite once per PR, narrowed reruns after.
  Run the integration suite in the background and keep working.
Subagents return results to you; they don't message anyone.

Gates (unchanged): no code before you approve the design; merge only when the PR
has "OL AC check @<SHA> … VERDICT: FULLY DONE" and the merged head is that SHA
plus develop merges only (git log --no-merges <SHA>..<head> ^origin/develop is
empty), the tester passed on it, security items are resolved, and
`git merge-base --is-ancestor origin/develop <head>` holds. Merge with
mcp__github__merge_pull_request, merge_method "merge", expectedHeadSha.

To get the OL check: send the supervisor "PR #<P> @<SHA> ready for AC check"; it
relays the verdict. Learnings line in CLAUDE.md goes in before that check.
Follow-ups: file them with the label "no-agent"; never start one.
Migration letter: <LETTER> (reserved by the supervisor).
When merged: rename this thread "✅ DONE: #<N> (PR #<P> merged)" and send the
supervisor one line. Past ~200k tokens of context, send a 15-line hand-off
instead and stop.

<RULES FOR EVERYONE from agent-team, with "archive the worker" replaced by
"nothing to archive: workers are subagents">
```

## Lessons

- Fewer hops: one thread per ticket replaces four sessions and their messages,
  and the supervisor relays only AC checks and merges.
