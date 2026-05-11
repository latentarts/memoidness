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
- an initial `adapter/cli` package built on top of `service.Service`
- an initial `adapter/rest` package built on top of `service.Service`
- an initial `adapter/rpc` package built on top of `service.Service`

## Immediate Next Step

Broaden and harden the first real host adapters.

Recommended order:

1. keep `adapter/cli`, `adapter/rest`, and `adapter/rpc` thin and make them depend only on `service.Service` plus transport/rendering code
2. validate and extend the shared lifecycle coverage through all adapters:
   - create/open/continue
   - prompt with streaming output or event delivery
   - steer/follow-up/abort
   - fork/clone/navigate
   - promote-skill
   - set-mode
3. add any missing service-facing support that both adapters need, especially around richer history inspection

## Design Constraints

- do not move transport concerns into `runtime`
- keep auth/scope resolution at the adapter edge
- keep event semantics identical to the runtime event stream
- prefer request/response translation over adapter-specific agent behavior

## After The First Adapter Trio

Once the CLI, REST, and RPC shapes are stable:

1. harden shared inspection and history surfaces
2. preserve the same `service.Service` operations and event semantics across all transports

## Still Open

- principal/global skill promotion
- distributed subagent execution
- richer child-session policy narrowing
- broader provider support
- deeper context discovery
