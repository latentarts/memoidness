# memoidness

`memoidness` is a library-first Go runtime for building coding agents.

It provides the core loop you need to run an agent against an arbitrary codebase:

- session-oriented runtime API
- OpenAI-compatible model provider support
- project instruction loading from local context files
- model-driven tool execution
- ordered runtime events
- resumable session persistence

The project is intended to be embedded inside another host application rather than used as a CLI by itself. A host can be a terminal app, HTTP service, RPC daemon, test harness, or any other Go process that needs coding-agent behavior.

## Current Status

The current implementation is an MVP backend runtime.

It is functional for:

- creating and reopening sessions
- loading repository guidance files before a turn
- sending prompts to an OpenAI-compatible chat-completions endpoint
- executing built-in filesystem and process tools under policy
- streaming ordered runtime events to subscribers
- persisting session history as append-only JSONL
- resolving scoped MCP-backed resources and tools
- promoting generated workspace skills explicitly
- forking, cloning, and replaying session history branches

It is not yet a full end-user product. There is still no built-in CLI, REST server, or RPC server in this repository, but the library now includes a thin adapter-facing `service` package intended to be the integration seam for those hosts.

## Installation

Requirements:

- Go `1.24`
- access to an OpenAI-compatible API endpoint

Add the module to your Go project:

```bash
go get github.com/latentarts/memoidness
```

## Integration Model

There are now two supported ways to embed `memoidness`:

1. direct runtime integration
2. adapter-oriented integration through `service.Service`

If you are building a CLI, REST API, RPC daemon, or any multi-request host, prefer `service.Service`. It owns session lookup and rebinding so adapters do not have to juggle `runtime.Session` objects directly.

The typical assembly pattern is:

1. resolve principal and workspace scope
2. configure a provider registry
3. configure durable session storage
4. configure filesystem and process policy
5. build `runtime.Runtime`
6. optionally wrap it in `service.Service`

## How To Use It In Another Codebase

### Recommended Host Wiring

This is the recommended integration point for a host adapter:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/provider"
	"github.com/latentarts/memoidness/runtime"
	"github.com/latentarts/memoidness/service"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

func main() {
	ctx := context.Background()

	targetRepo := "/path/to/another/codebase"
	storeDir := filepath.Join(targetRepo, ".memoidness", "sessions")

	sessionManager, err := session.NewJSONLManager(storeDir)
	if err != nil {
		log.Fatal(err)
	}

	modelProvider := provider.NewOpenAICompatibleProvider(provider.OpenAIConfig{
		ID:      "local-openai",
		BaseURL: "https://your-openai-compatible-host/v1",
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Client:  http.DefaultClient,
	})

	rt := runtime.New(runtime.Config{
		Providers:      provider.NewStaticRegistry("local-openai", modelProvider),
		SessionManager: sessionManager,
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{
				ReadableRoots: []string{targetRepo},
				WritableRoots: []string{targetRepo},
			},
			Process: policy.ProcessPolicy{
				AllowedCommands: []string{
					"go test",
					"go build",
					"git status",
					"rg",
				},
			},
		},
	})

	svc := service.New(rt, sessionManager)

	snapshot, err := svc.CreateSession(ctx, service.CreateSessionRequest{
		Options: runtime.SessionOptions{
			Model: types.ModelRef{
				ProviderID: "local-openai",
				ID:         "gpt-4.1-mini",
			},
			Scope: types.SessionScope{
				Principal: types.PrincipalRef{ID: "user-1"},
				Workspace: types.WorkspaceSpec{
					Ref:        types.WorkspaceRef{ID: "repo-1"},
					Kind:       "local",
					WorkingDir: targetRepo,
				},
			},
			Persistence: types.PersistenceModeSession,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ref := types.SessionRef{
		ID:        snapshot.SessionID,
		Principal: snapshot.Scope.Principal.ID,
		Workspace: snapshot.Scope.Workspace.Ref.ID,
	}

	_, err = svc.Subscribe(ctx, ref, func(ev any) {
		fmt.Printf("event: %#v\n", ev)
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := svc.Prompt(ctx, ref, types.UserInput{
		Text: "Read the repository instructions, inspect the project, and explain the test layout.",
	}, types.PromptOptions{
		Stream: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(result.FinalOutput.Parts[0].Text)
}
```

### Direct Runtime Integration

If you are embedding `memoidness` inside a single in-process application and you do not need a session coordinator, you can still use `runtime.Runtime` directly:

```go
sess, err := rt.NewSession(ctx, runtime.SessionOptions{
	Model: types.ModelRef{
		ProviderID: "local-openai",
		ID:         "gpt-4.1-mini",
	},
	Scope: types.SessionScope{
		Principal: types.PrincipalRef{ID: "user-1"},
		Workspace: types.WorkspaceSpec{
			Ref:        types.WorkspaceRef{ID: "repo-1"},
			Kind:       "local",
			WorkingDir: targetRepo,
		},
	},
	Persistence: types.PersistenceModeSession,
})
if err != nil {
	log.Fatal(err)
}
```

### Reopening Or Continuing A Session Through The Service Layer

```go
snapshot, err := svc.OpenSession(ctx, types.SessionRef{
	ID:        "session-123",
	Principal: "user-1",
	Workspace: "repo-1",
})
if err != nil {
	log.Fatal(err)
}

recent, err := svc.ContinueRecent(ctx, service.ContinueRecentRequest{
	Scope: session.Scope{
		Principal: "user-1",
		Workspace: "repo-1",
	},
})
if err != nil {
	log.Fatal(err)
}
_ = snapshot
_ = recent
```

### Branch Operations Through The Service Layer

```go
forked, err := svc.Fork(ctx, ref, types.EntryRef{ID: "msg-5"})
if err != nil {
	log.Fatal(err)
}

cloned, err := svc.Clone(ctx, ref)
if err != nil {
	log.Fatal(err)
}

navigated, err := svc.Navigate(ctx, ref, types.EntryRef{ID: "msg-5"})
if err != nil {
	log.Fatal(err)
}

_ = forked
_ = cloned
_ = navigated
```

`Fork` and `Navigate` accept ids that come back from visible message or tool history; the runtime resolves them back to persisted entry ids before handing them to the session manager.

## What The Runtime Loads From The Target Repository

Before the first prompt in a session, the default filesystem resource loader scans the working directory and optional context roots for instruction files:

- `AGENTS.md`
- `CODEX.md`
- `CLAUDE.md`
- `CURSOR.md`
- `GEMINI.md`
- `PI.md`

These files are loaded into the model request as system instructions. Stop paths from `policy.ResourcePolicy.StopPaths` are respected during discovery.

The default loader also discovers promoted workspace skills under:

- `.memoidness/skills/*.md`

## Built-In Functionality

### 1. Session Runtime

The runtime exposes:

- `NewSession`
- `OpenSession`
- `Prompt`
- `Steer`
- `FollowUp`
- `Abort`
- `Fork`
- `Clone`
- `Navigate`
- `PromoteSkill`
- `Compact`
- `Subscribe`
- `Snapshot`

Each session runs through a serialized internal loop so prompts, tool execution, persistence, and event emission all happen through one authoritative path.

### 2. OpenAI-Compatible Provider

The built-in provider supports:

- custom base URL
- API key injection
- standard chat completions
- streaming assistant text deltas
- tool-call decoding from model responses

Use it when your model endpoint exposes an OpenAI-style `/chat/completions` API.

### 3. Adapter-Facing Service Layer

The `service` package provides a thin session coordinator for host adapters.

It adds:

- session caching by session id
- `CreateSession`
- `OpenSession`
- `ContinueRecent`
- `Prompt`
- `Steer`
- `FollowUp`
- `Abort`
- `Fork`
- `Clone`
- `Navigate`
- `PromoteSkill`
- `SetMode`
- `Snapshot`
- `Subscribe`

This keeps CLI, REST, and RPC adapters thin and transport-focused while preserving the runtime as the single owner of behavior.

### 4. Built-In Tools

The default tool registry includes:

- `read_file`
- `write_file`
- `exec`

`read_file` reads UTF-8 files under allowed readable roots.

`write_file` writes or appends UTF-8 text under allowed writable roots.

`exec` runs an allowed process and streams `stdout` and `stderr` updates through runtime events.

### 5. Capability And MCP Integration

The runtime now supports:

- mode-aware capability resolution
- same-process subagent orchestration
- scoped MCP-backed resource and tool providers
- explicit queue semantics for `Steer` and `FollowUp`

MCP integrations are optional and are wired through `Config.MCPRegistry`.

### 6. Policy Enforcement

The runtime expects the embedding host to define policy explicitly:

- `Filesystem.ReadableRoots`
- `Filesystem.WritableRoots`
- `Process.AllowedCommands`
- `Resources.StopPaths`

If a tool call violates policy, the tool result is returned as a structured error instead of bypassing the runtime.

### 7. Persistent Sessions

The JSONL session manager stores append-only session history on disk.

Current supported durable operations:

- create session
- append entries
- open session by id
- continue the most recent session for a principal/workspace scope
- list sessions
- fork a new branch from current or prior history
- clone a session into a new branch
- replay a session to a prior entry with `Navigate`

This is enough to resume work across process restarts and preserve turn history for later inspection.

### 8. Generated Skill Promotion

When no matching skill exists, the runtime can synthesize an ephemeral generated skill for the current session.

That skill remains session-scoped until explicitly promoted. The default promoter writes workspace skills under `.memoidness/skills`.

Current support:

- ephemeral generated skills
- explicit promotion through `Session.PromoteSkill` or `service.Service.PromoteSkill`
- durable workspace skill rediscovery in later sessions

Not yet supported:

- principal-level promotion
- global promotion
- external approval-service integration

### 9. Event Streaming

Hosts can subscribe to ordered runtime events with `Session.Subscribe`.

Current event families include:

- `agent_start`
- `turn_start`
- `message_delta`
- `message_complete`
- `queue_update`
- `tool_execution_start`
- `tool_execution_update`
- `tool_execution_end`
- `capability_resolution`
- `capability_denial`
- `mcp_server_resolution`
- `mcp_server_session_start`
- `mcp_server_session_end`
- `skill_promotion`
- `subagent_start`
- `subagent_end`
- `session_fork`
- `session_clone`
- `session_navigate`
- `turn_end`
- `agent_end`
- `error`

This is the main integration surface for live UIs, logs, streaming APIs, and host-side orchestration.

## Recommended Host Integration Pattern

For another codebase, the recommended pattern is:

1. store sessions under a project-local directory such as `.memoidness/sessions`
2. resolve explicit `Principal` and `Workspace` ids per host request
3. assemble one `runtime.Runtime` with injected provider, storage, policy, capability, and optional MCP dependencies
4. wrap it in one long-lived `service.Service`
5. keep adapters transport-only: translate inbound requests into `service` calls and stream normalized runtime events back out
6. restrict file and process permissions aggressively
7. use `Prompt(..., Stream: true)` for interactive hosts

If you are building adapters, the intended dependency direction is:

- `runtime`, `session`, `resources`, `tools`, `provider`, `capabilities`, `mcp`, `types`
- then `service`
- then adapter packages such as `cli`, `rest`, or `rpc`

The adapters should not own agent behavior. They should only:

- resolve scope and auth at the edge
- serialize and deserialize transport payloads
- subscribe to runtime events
- call `service.Service`

## Roadmap

Still pending:

- built-in CLI adapter
- built-in REST adapter
- built-in RPC adapter
- principal/global skill promotion targets
- deeper child-session policy narrowing beyond tool allowlists
- distributed or remote subagent execution

## Next Session

If you are picking this up next, the recommended order is:

1. add the first real adapter package under an `adapter` tree
2. make `CLI` the first adapter using `service.Service` as its only runtime integration point
3. validate session lifecycle coverage in the CLI:
   - create/open/continue
   - prompt/stream
   - steer/follow-up/abort
   - fork/clone/navigate
   - promote-skill
4. once the CLI shape is stable, build `REST` and `RPC` adapters against the same service surface

## Testing

Run the test suite with:

```bash
env GOCACHE=/tmp/memoidness-gocache go test ./...
```

In restricted environments, the explicit `GOCACHE` override may be necessary if the default Go build cache path is not writable.

## Limitations

Current MVP limitations:

- no built-in CLI
- no REST or RPC adapter
- no built-in adapter packages yet; only the library and `service` integration layer exist
- no principal/global skill promotion yet
- no distributed subagent execution yet
- no provider families beyond OpenAI-compatible chat-completions APIs
- context discovery is still intentionally small and centered on top-level instruction files plus workspace skills

## Summary

`memoidness` is currently a usable backend runtime for embedding coding-agent behavior into another Go application. If you need a Go-native session loop with policy-gated tools, repository instruction loading, resumable sessions, and ordered events, the current codebase is ready to integrate. If you need a packaged end-user agent product, host adapters and advanced session semantics are still pending.
