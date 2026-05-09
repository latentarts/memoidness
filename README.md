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

It is not yet a full end-user product. There is no built-in CLI, REST server, or RPC adapter in this repository yet.

## Installation

Requirements:

- Go `1.24`
- access to an OpenAI-compatible API endpoint

Add the module to your Go project:

```bash
go get github.com/latentarts/memoidness
```

## How To Use It In Another Codebase

The typical embedding pattern is:

1. point the runtime at the target repository with `WorkingDir`
2. configure a provider registry
3. configure durable session storage
4. configure filesystem and process policy
5. create a session and call `Prompt`

### Minimal Example

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

	sess, err := rt.NewSession(ctx, runtime.SessionOptions{
		WorkingDir:  targetRepo,
		Model:       types.ModelRef{ID: "gpt-4.1-mini"},
		Persistence: types.PersistenceModeSession,
	})
	if err != nil {
		log.Fatal(err)
	}

	sess.Subscribe(func(ev any) {
		fmt.Printf("event: %#v\n", ev)
	})

	result, err := sess.Prompt(ctx, types.UserInput{
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

### Reopening An Existing Session

```go
sess, err := rt.OpenSession(ctx, types.SessionRef{ID: "session-123"})
if err != nil {
	log.Fatal(err)
}
```

### Continuing The Most Recent Session For A Repository

This is supported at the `session.Manager` level through `ContinueRecent`. If you need that behavior in a host, call the manager directly and then reopen the returned session id through the runtime.

## What The Runtime Loads From The Target Repository

Before the first prompt in a session, the default filesystem resource loader scans the working directory and optional context roots for instruction files:

- `AGENTS.md`
- `CODEX.md`
- `CLAUDE.md`
- `CURSOR.md`
- `GEMINI.md`
- `PI.md`

These files are loaded into the model request as system instructions. Stop paths from `policy.ResourcePolicy.StopPaths` are respected during discovery.

## Built-In Functionality

### 1. Session Runtime

The runtime exposes:

- `NewSession`
- `OpenSession`
- `Prompt`
- `Abort`
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

### 3. Built-In Tools

The default tool registry includes:

- `read_file`
- `write_file`
- `exec`

`read_file` reads UTF-8 files under allowed readable roots.

`write_file` writes or appends UTF-8 text under allowed writable roots.

`exec` runs an allowed process and streams `stdout` and `stderr` updates through runtime events.

### 4. Policy Enforcement

The runtime expects the embedding host to define policy explicitly:

- `Filesystem.ReadableRoots`
- `Filesystem.WritableRoots`
- `Process.AllowedCommands`
- `Resources.StopPaths`

If a tool call violates policy, the tool result is returned as a structured error instead of bypassing the runtime.

### 5. Persistent Sessions

The JSONL session manager stores append-only session history on disk.

Current supported durable operations:

- create session
- append entries
- open session by id
- continue the most recent session for a working directory
- list sessions

This is enough to resume work across process restarts and preserve turn history for later inspection.

### 6. Event Streaming

Hosts can subscribe to ordered runtime events with `Session.Subscribe`.

Current event families include:

- `agent_start`
- `turn_start`
- `message_delta`
- `message_complete`
- `tool_execution_start`
- `tool_execution_update`
- `tool_execution_end`
- `turn_end`
- `agent_end`
- `error`

This is the main integration surface for live UIs, logs, streaming APIs, and host-side orchestration.

## Recommended Host Integration Pattern

For another codebase, a good starting pattern is:

1. store sessions under a project-local directory such as `.memoidness/sessions`
2. set `WorkingDir` to the target repository root
3. restrict file and process permissions aggressively
4. subscribe to events and stream them to your UI or logs
5. use `Prompt(..., Stream: true)` for the best user experience

If you are embedding this in a service, keep the runtime assembly in one place and treat provider, session storage, policy, and resource loading as injected dependencies.

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
- no branch-aware session tree
- no durable `Fork`, `Clone`, or `Navigate`
- no active-run `Steer` or `FollowUp` support
- no provider families beyond OpenAI-compatible chat-completions APIs
- context discovery is currently limited to a small set of top-level instruction files

## Roadmap

### Near Term

- implement queued `Steer` and `FollowUp` controls during active runs
- add stronger typed event payload documentation and host-facing examples
- expand resource loading beyond top-level instruction files
- harden provider error handling and response validation
- add better process sandboxing and richer execution policy

### Next Major Runtime Features

- durable branch-aware session history
- real `Fork`, `Clone`, and `Navigate` behavior
- explicit replay and restore utilities
- structured compaction and summarization hooks
- richer built-in tool set for code editing workflows

### Host Surfaces

- minimal CLI host
- JSONL or streaming RPC adapter
- REST API and server-sent event or equivalent event streaming adapter

### Longer-Term Direction

- broader provider support beyond OpenAI-compatible endpoints
- more complete project context discovery
- extension and plugin-style resource loading
- browser-facing or remote host adapters built on the same runtime event model

## Summary

`memoidness` is currently a usable backend runtime for embedding coding-agent behavior into another Go application. If you need a Go-native session loop with policy-gated tools, repository instruction loading, resumable sessions, and ordered events, the current codebase is ready to integrate. If you need a packaged end-user agent product, host adapters and advanced session semantics are still pending.
