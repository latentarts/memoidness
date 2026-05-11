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
- expanded README guidance for direct runtime use, `service.Service`, and all three adapters

## Immediate Next Step

Harden the shared host surface behind the adapter trio.

Recommended order:

1. add richer service-facing inspection and history support that all adapters can reuse
2. keep `adapter/cli`, `adapter/rest`, and `adapter/rpc` thin while exposing that shared surface
3. tighten transport behavior where the current first pass is intentionally minimal:
   - CLI output shaping
   - REST request and response polish
   - RPC framing and subscription ergonomics
4. preserve one behavior model across runtime, service, and all transports

## Design Constraints

- do not move transport concerns into `runtime`
- do not let adapter-specific convenience logic become a second orchestration path
- keep auth/scope resolution at the adapter edge
- keep event semantics identical to the runtime event stream
- prefer request/response translation over adapter-specific agent behavior

## After Shared Inspection

Once shared inspection and history surfaces are in place:

1. harden the adapter trio against real host use
2. add examples or runnable `examples/` programs if needed
3. evaluate broader provider support and deeper context loading

## Still Open

- richer history inspection and listing across adapters
- principal/global skill promotion
- distributed subagent execution
- richer child-session policy narrowing
- broader provider support
- deeper context discovery
