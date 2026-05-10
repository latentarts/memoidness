# Next Steps

This file is the short handoff for the next session.

## Current State

The runtime core now has:

- scoped principal/workspace sessions
- mode-aware capability resolution
- MCP-backed resource and tool integration
- generated-skill promotion to workspace scope
- same-process subagents with orchestration gating and child tool narrowing
- durable session branching through `Fork`, `Clone`, and `Navigate`
- a thin adapter-facing `service.Service` integration layer

## Immediate Next Step

Build the first real host adapter.

Recommended order:

1. add `adapter/cli`
2. keep it thin and make it depend only on `service.Service` plus transport/rendering code
3. validate the full lifecycle through the CLI:
   - create/open/continue
   - prompt with streaming output
   - steer/follow-up/abort
   - fork/clone/navigate
   - promote-skill
   - set-mode

## Design Constraints

- do not move transport concerns into `runtime`
- keep auth/scope resolution at the adapter edge
- keep event semantics identical to the runtime event stream
- prefer request/response translation over adapter-specific agent behavior

## After CLI

Once the CLI shape is stable:

1. add `adapter/rest`
2. add `adapter/rpc`
3. reuse the same `service.Service` operations and event semantics

## Still Open

- principal/global skill promotion
- distributed subagent execution
- richer child-session policy narrowing
- broader provider support
- deeper context discovery
