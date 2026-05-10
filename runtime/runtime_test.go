package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/latentarts/memoidness/capabilities"
	"github.com/latentarts/memoidness/events"
	"github.com/latentarts/memoidness/mcp"
	"github.com/latentarts/memoidness/policy"
	"github.com/latentarts/memoidness/provider"
	"github.com/latentarts/memoidness/session"
	"github.com/latentarts/memoidness/types"
)

func TestValidateRequiresProviderRegistry(t *testing.T) {
	rt := New(Config{})
	if err := rt.Validate(context.Background()); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestPromptExecutesSingleTurn(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})

	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "ack: hello" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestPromptStreamingEmitsDeltaEvents(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	var last events.Envelope
	sess.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		mu.Lock()
		seen = append(seen, envelope.Type)
		last = envelope
		mu.Unlock()
	})

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{Stream: true})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "ack: hello" {
		t.Fatalf("unexpected output: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !contains(seen, "message_delta") {
		t.Fatalf("expected message_delta event, got %v", seen)
	}
	if last.Principal != "principal-1" || last.Workspace != "workspace-1" {
		t.Fatalf("expected scoped event metadata, got %+v", last)
	}
}

func TestPromptToolLoopExecutesBuiltinTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	rt := New(Config{
		Providers: provider.NewStaticRegistry("tool", toolLoopProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
		},
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "read it"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "done after tool" {
		t.Fatalf("unexpected final output: %q", got)
	}
	if got := len(result.Snapshot.Messages); got < 4 {
		t.Fatalf("expected persisted tool loop history, got %d messages", got)
	}
}

func TestAbortCancelsRunningPrompt(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("slow", slowProvider{}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := sess.Abort(context.Background()); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected prompt cancellation error")
	}
}

func TestForkCloneAndNavigateThroughRuntime(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	first, err := sess.Prompt(context.Background(), types.UserInput{Text: "one"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	second, err := sess.Prompt(context.Background(), types.UserInput{Text: "two"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	targetID := first.Snapshot.Messages[0].ID

	forked, err := sess.Fork(context.Background(), types.EntryRef{ID: targetID})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	forkedSnapshot := forked.Snapshot()
	if forkedSnapshot.SessionID == second.Snapshot.SessionID {
		t.Fatalf("expected forked session id to differ, got %q", forkedSnapshot.SessionID)
	}
	if forkedSnapshot.BranchID == second.Snapshot.BranchID {
		t.Fatalf("expected forked branch id to differ, got %q", forkedSnapshot.BranchID)
	}
	if len(forkedSnapshot.Messages) != 1 || forkedSnapshot.Messages[0].ID != targetID {
		t.Fatalf("unexpected forked messages: %+v", forkedSnapshot.Messages)
	}

	cloned, err := sess.Clone(context.Background())
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	clonedSnapshot := cloned.Snapshot()
	if len(clonedSnapshot.Messages) != len(second.Snapshot.Messages) {
		t.Fatalf("expected cloned history length %d, got %d", len(second.Snapshot.Messages), len(clonedSnapshot.Messages))
	}

	if err := sess.Navigate(context.Background(), types.EntryRef{ID: targetID}); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	navigated := sess.Snapshot()
	if len(navigated.Messages) != 1 || navigated.Messages[0].ID != targetID {
		t.Fatalf("unexpected navigated messages: %+v", navigated.Messages)
	}

	continued, err := sess.Prompt(context.Background(), types.UserInput{Text: "after navigate"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("continued prompt: %v", err)
	}
	if got := continued.FinalOutput.Parts[0].Text; got != "ack: after navigate" {
		t.Fatalf("unexpected continued output: %q", got)
	}
}

func TestBranchOperationsRequireIdleSession(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("slow", slowProvider{}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), types.UserInput{Text: "hello"}, types.PromptOptions{})
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := sess.Clone(context.Background()); err != ErrSessionBusy {
		t.Fatalf("expected ErrSessionBusy for clone, got %v", err)
	}
	if _, err := sess.Fork(context.Background(), types.EntryRef{ID: "entry-1"}); err != ErrSessionBusy {
		t.Fatalf("expected ErrSessionBusy for fork, got %v", err)
	}
	if err := sess.Navigate(context.Background(), types.EntryRef{ID: "entry-1"}); err != ErrSessionBusy {
		t.Fatalf("expected ErrSessionBusy for navigate, got %v", err)
	}
	if err := sess.Abort(context.Background()); err != nil {
		t.Fatalf("abort: %v", err)
	}
	<-errCh
}

func TestSteerAndFollowUpRequireActiveRun(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := sess.Steer(context.Background(), types.UserInput{Text: "x"}); err != ErrSessionNotRunning {
		t.Fatalf("expected ErrSessionNotRunning for steer, got %v", err)
	}
	if err := sess.FollowUp(context.Background(), types.UserInput{Text: "x"}); err != ErrSessionNotRunning {
		t.Fatalf("expected ErrSessionNotRunning for follow-up, got %v", err)
	}
}

func TestSessionSnapshotCarriesScopeAndMode(t *testing.T) {
	scope := testScope(t.TempDir())
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: scope,
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	snapshot := sess.Snapshot()
	if snapshot.Scope.Principal.ID != scope.Principal.ID {
		t.Fatalf("unexpected principal: %+v", snapshot.Scope)
	}
	if snapshot.Mode.ID != "plan" {
		t.Fatalf("unexpected mode: %+v", snapshot.Mode)
	}
}

func TestPlanModeNarrowsVisibleTools(t *testing.T) {
	var seen []string
	rt := New(Config{
		Providers: provider.NewStaticRegistry("visible-tools", visibleToolsProvider{seen: &seen}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), types.UserInput{Text: "inspect"}, types.PromptOptions{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"read_file"}) {
		t.Fatalf("unexpected visible tools in plan mode: %v", seen)
	}
}

func TestPlanModeDeniesMutatingToolExecution(t *testing.T) {
	root := t.TempDir()
	rt := New(Config{
		Providers: provider.NewStaticRegistry("forbidden-tool", forbiddenToolProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{
				ReadableRoots: []string{root},
				WritableRoots: []string{root},
			},
		},
	})
	var seen []string
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	sess.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		seen = append(seen, envelope.Type)
	})
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "try write"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "tool denied handled" {
		t.Fatalf("unexpected final output: %q", got)
	}
	if !contains(seen, "capability_denial") {
		t.Fatalf("expected capability_denial event, got %v", seen)
	}
}

func TestPlanModeLoadsCapabilityResources(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "create a plan"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var sawGenerated bool
	for _, diag := range result.Diagnostics {
		if diag.Code == "generated_skill_created" {
			sawGenerated = true
		}
	}
	if sawGenerated {
		t.Fatalf("did not expect generated skill when plan resources provide a skill, got %+v", result.Diagnostics)
	}
}

func TestRuntimeLoadsMCPResourcesAndEmitsLifecycle(t *testing.T) {
	manager := session.NewInMemoryManager()
	rt := New(Config{
		Providers:      provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		SessionManager: manager,
		MCPRegistry: mcp.NewStaticRegistry(mcp.StaticServer{
			Provider:     mcpTestProvider{},
			Enabled:      true,
			AllowedModes: []string{"plan", "implementation"},
		}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	var seen []string
	sess.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		seen = append(seen, envelope.Type)
	})
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "inspect mcp"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var sawMCP bool
	for _, diag := range result.Diagnostics {
		if diag.Code == "mcp_provider_loaded" {
			sawMCP = true
		}
	}
	if !sawMCP {
		t.Fatalf("expected MCP diagnostic, got %+v", result.Diagnostics)
	}
	if !contains(seen, "mcp_server_resolution") || !contains(seen, "mcp_server_session_start") || !contains(seen, "mcp_server_session_end") {
		t.Fatalf("expected MCP lifecycle events, got %v", seen)
	}
	record, err := manager.Open(context.Background(), types.SessionRef{
		ID:        result.Snapshot.SessionID,
		Principal: result.Snapshot.Scope.Principal.ID,
		Workspace: result.Snapshot.Scope.Workspace.Ref.ID,
	})
	if err != nil {
		t.Fatalf("open session record: %v", err)
	}
	var sawResolutionEntry bool
	for _, entry := range record.Entries {
		if entry.Kind == "mcp_server_resolution" && len(entry.MCPServers) == 1 && entry.MCPServers[0].Ref.ID == "mcp-test" {
			sawResolutionEntry = true
			break
		}
	}
	if !sawResolutionEntry {
		t.Fatalf("expected mcp_server_resolution entry, got %+v", record.Entries)
	}
}

func TestMCPToolIsVisibleAndExecutesThroughRuntime(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("mcp-tool-loop", mcpToolLoopProvider{}),
		MCPRegistry: mcp.NewStaticRegistry(mcp.StaticServer{
			Provider:     mcpTestProvider{},
			Enabled:      true,
			AllowedModes: []string{"implementation"},
		}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "use mcp tool"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := result.FinalOutput.Parts[0].Text; got != "done after mcp tool" {
		t.Fatalf("unexpected final output: %q", got)
	}
	snapshot := result.Snapshot
	var sawToolResult bool
	for _, msg := range snapshot.Messages {
		if msg.Role != "tool" {
			continue
		}
		for _, part := range msg.Parts {
			if part.Kind == "tool_result" && part.Result != nil && part.Result.CallID == "mcp-tool-1" {
				sawToolResult = true
			}
		}
	}
	if !sawToolResult {
		t.Fatalf("expected MCP tool result in snapshot: %+v", snapshot.Messages)
	}
}

func TestDisabledMCPRegistryDoesNotContributeResourcesOrTools(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		MCPRegistry: mcp.NewStaticRegistry(mcp.StaticServer{
			Provider: mcpTestProvider{},
			Enabled:  false,
		}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "write tests"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == "mcp_provider_loaded" {
			t.Fatalf("did not expect MCP diagnostics when disabled, got %+v", result.Diagnostics)
		}
	}
}

func TestImplementationModeGeneratesEphemeralSkillWhenMissing(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "write tests"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var sawGenerated bool
	for _, diag := range result.Diagnostics {
		if diag.Code == "generated_skill_created" {
			sawGenerated = true
		}
	}
	if !sawGenerated {
		t.Fatalf("expected generated skill diagnostic, got %+v", result.Diagnostics)
	}
}

func TestPromoteGeneratedSkillPersistsWorkspaceSkill(t *testing.T) {
	root := t.TempDir()
	manager := session.NewInMemoryManager()
	rt := New(Config{
		Providers:      provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		SessionManager: manager,
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := sess.Prompt(context.Background(), types.UserInput{Text: "write tests"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	promoted, err := sess.PromoteSkill(context.Background(), types.SkillPromotionRequest{
		Name:   "generated-write-tests",
		Target: types.SkillPromotionTargetWorkspace,
	})
	if err != nil {
		t.Fatalf("promote skill: %v", err)
	}
	if promoted.Target != types.SkillPromotionTargetWorkspace {
		t.Fatalf("unexpected promotion target: %+v", promoted)
	}
	if _, err := os.Stat(promoted.Path); err != nil {
		t.Fatalf("expected promoted skill file: %v", err)
	}
	record, err := manager.Open(context.Background(), types.SessionRef{
		ID:        result.Snapshot.SessionID,
		Principal: result.Snapshot.Scope.Principal.ID,
		Workspace: result.Snapshot.Scope.Workspace.Ref.ID,
	})
	if err != nil {
		t.Fatalf("open record: %v", err)
	}
	var sawPromotion bool
	for _, entry := range record.Entries {
		if entry.Kind == "skill_promotion" && entry.SkillPromotion != nil && entry.SkillPromotion.Name == "generated-write-tests" {
			sawPromotion = true
			break
		}
	}
	if !sawPromotion {
		t.Fatalf("expected skill_promotion entry, got %+v", record.Entries)
	}

	nextSess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new next session: %v", err)
	}
	nextResult, err := nextSess.Prompt(context.Background(), types.UserInput{Text: "write tests"}, types.PromptOptions{})
	if err != nil {
		t.Fatalf("next prompt: %v", err)
	}
	for _, diag := range nextResult.Diagnostics {
		if diag.Code == "generated_skill_created" {
			t.Fatalf("did not expect generated skill after promotion, got %+v", nextResult.Diagnostics)
		}
	}
}

func TestPromoteSkillRejectsMissingOrUnsupportedTarget(t *testing.T) {
	root := t.TempDir()
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := sess.PromoteSkill(context.Background(), types.SkillPromotionRequest{
		Name:   "missing-skill",
		Target: types.SkillPromotionTargetWorkspace,
	}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected missing skill error, got %v", err)
	}
	if _, err := sess.Prompt(context.Background(), types.UserInput{Text: "write tests"}, types.PromptOptions{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if _, err := sess.PromoteSkill(context.Background(), types.SkillPromotionRequest{
		Name:   "generated-write-tests",
		Target: types.SkillPromotionTargetPrincipal,
	}); err == nil {
		t.Fatal("expected unsupported promotion target error")
	}
}

func TestSpawnSubagentInheritsScopeAndEmitsLifecycle(t *testing.T) {
	manager := session.NewInMemoryManager()
	rt := New(Config{
		Providers:      provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		SessionManager: manager,
	})
	parent, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new parent session: %v", err)
	}
	var seen []string
	parent.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		seen = append(seen, envelope.Type)
	})
	child, err := parent.SpawnSubagent(context.Background(), types.SubagentRequest{
		Input: types.UserInput{Text: "inspect child task"},
	})
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if child.Run.Snapshot.Scope.Principal.ID != "principal-1" || child.Run.Snapshot.Scope.Workspace.Ref.ID != "workspace-1" {
		t.Fatalf("unexpected inherited scope: %+v", child.Run.Snapshot.Scope)
	}
	if child.Run.Snapshot.Mode.ID != "plan" {
		t.Fatalf("unexpected inherited mode: %+v", child.Run.Snapshot.Mode)
	}
	if !contains(seen, "subagent_start") || !contains(seen, "subagent_end") {
		t.Fatalf("expected subagent lifecycle events, got %v", seen)
	}
	if got := child.Run.FinalOutput.Parts[0].Text; got != "ack: inspect child task" {
		t.Fatalf("unexpected child output: %q", got)
	}
	parentSnapshot := parent.Snapshot()
	parentRecord, err := manager.Open(context.Background(), types.SessionRef{
		ID:        parentSnapshot.SessionID,
		Principal: parentSnapshot.Scope.Principal.ID,
		Workspace: parentSnapshot.Scope.Workspace.Ref.ID,
	})
	if err != nil {
		t.Fatalf("open parent record: %v", err)
	}
	var sawLink bool
	for _, entry := range parentRecord.Entries {
		if entry.Kind == "subagent_link" && entry.Subagent != nil && entry.Subagent.ID == child.SessionRef.ID {
			sawLink = true
			break
		}
	}
	if !sawLink {
		t.Fatalf("expected parent subagent link entry, got %+v", parentRecord.Entries)
	}
	childRecord, err := manager.Open(context.Background(), child.SessionRef)
	if err != nil {
		t.Fatalf("open child record: %v", err)
	}
	var sawParent bool
	for _, entry := range childRecord.Entries {
		if entry.Kind == "subagent_parent" && entry.ParentSession != nil && entry.ParentSession.ID == parentSnapshot.SessionID {
			sawParent = true
			break
		}
	}
	if !sawParent {
		t.Fatalf("expected child parent linkage entry, got %+v", childRecord.Entries)
	}
}

func TestSpawnSubagentRequiresOrchestrationCapability(t *testing.T) {
	rt := New(Config{
		Providers: provider.NewStaticRegistry("stub", stubProvider{id: "stub"}),
		Capabilities: noSubagentRegistry{
			resolved: capabilities.Resolved{
				Mode:         types.ModeRef{ID: "implementation"},
				AllowedTools: map[string]struct{}{"read_file": {}},
			},
		},
	})
	parent, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(t.TempDir()),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new parent session: %v", err)
	}
	if _, err := parent.SpawnSubagent(context.Background(), types.SubagentRequest{
		Input: types.UserInput{Text: "inspect child task"},
	}); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("expected capability denial, got %v", err)
	}
}

func TestSpawnSubagentCanNarrowChildTools(t *testing.T) {
	root := t.TempDir()
	manager := session.NewInMemoryManager()
	rt := New(Config{
		Providers:      provider.NewStaticRegistry("forbidden-tool", forbiddenToolProvider{}),
		SessionManager: manager,
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{
				ReadableRoots: []string{root},
				WritableRoots: []string{root},
			},
		},
	})
	parent, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new parent session: %v", err)
	}
	child, err := parent.SpawnSubagent(context.Background(), types.SubagentRequest{
		Input:         types.UserInput{Text: "try write"},
		ToolAllowlist: []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if got := child.Run.FinalOutput.Parts[0].Text; got != "tool denied handled" {
		t.Fatalf("unexpected child output: %q", got)
	}
	parentSnapshot := parent.Snapshot()
	parentRecord, err := manager.Open(context.Background(), types.SessionRef{
		ID:        parentSnapshot.SessionID,
		Principal: parentSnapshot.Scope.Principal.ID,
		Workspace: parentSnapshot.Scope.Workspace.Ref.ID,
	})
	if err != nil {
		t.Fatalf("open parent record: %v", err)
	}
	var sawAllowlist bool
	for _, entry := range parentRecord.Entries {
		if entry.Kind == "subagent_link" && slices.Equal(entry.ToolAllowlist, []string{"read_file"}) {
			sawAllowlist = true
			break
		}
	}
	if !sawAllowlist {
		t.Fatalf("expected persisted tool allowlist on subagent link, got %+v", parentRecord.Entries)
	}
}

func TestFollowUpQueuesIntoActiveRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	rt := New(Config{
		Providers: provider.NewStaticRegistry("queued-input", queuedInputProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
		},
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "plan"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	resultCh := make(chan types.RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := sess.Prompt(context.Background(), types.UserInput{Text: "start"}, types.PromptOptions{})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	time.Sleep(20 * time.Millisecond)
	if err := sess.FollowUp(context.Background(), types.UserInput{Text: "queued work"}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("prompt failed: %v", err)
	case result := <-resultCh:
		if got := result.FinalOutput.Parts[0].Text; got != "queued: queued work" {
			t.Fatalf("unexpected final output: %q", got)
		}
		var sawQueued bool
		for _, msg := range result.Snapshot.Messages {
			if msg.Role == "user" && msg.ProviderMeta != nil && msg.ProviderMeta["queue_kind"] == "follow_up" {
				sawQueued = true
				break
			}
		}
		if !sawQueued {
			t.Fatalf("expected queued follow-up to be persisted in snapshot: %+v", result.Snapshot.Messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued run result")
	}
}

func TestSteerQueuesDeveloperInputIntoActiveRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	rt := New(Config{
		Providers: provider.NewStaticRegistry("queued-input", queuedInputProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
		},
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	resultCh := make(chan types.RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := sess.Prompt(context.Background(), types.UserInput{Text: "start"}, types.PromptOptions{})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	time.Sleep(20 * time.Millisecond)
	if err := sess.Steer(context.Background(), types.UserInput{Text: "focus on tests"}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("prompt failed: %v", err)
	case result := <-resultCh:
		if got := result.FinalOutput.Parts[0].Text; got != "steered: focus on tests" {
			t.Fatalf("unexpected final output: %q", got)
		}
		var sawSteer bool
		for _, msg := range result.Snapshot.Messages {
			if msg.Role == "developer" && msg.ProviderMeta != nil && msg.ProviderMeta["queue_kind"] == "steer" {
				sawSteer = true
				break
			}
		}
		if !sawSteer {
			t.Fatalf("expected queued steer to be persisted as a developer message: %+v", result.Snapshot.Messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for steered run result")
	}
}

func TestQueuedInputsEmitQueueLifecycleEvents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	rt := New(Config{
		Providers: provider.NewStaticRegistry("queued-input", queuedInputProvider{}),
		Policy: policy.RuntimePolicy{
			Filesystem: policy.FilesystemPolicy{ReadableRoots: []string{root}},
		},
	})
	sess, err := rt.NewSession(context.Background(), SessionOptions{
		Model: types.ModelRef{ID: "test-model"},
		Scope: testScope(root),
		Mode:  types.ModeRef{ID: "implementation"},
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	type queueEvent struct {
		Action  string
		Kind    string
		Pending int
		Count   int
	}
	var seen []queueEvent
	var mu sync.Mutex
	sess.Subscribe(func(ev events.RuntimeEvent) {
		envelope := ev.(events.Envelope)
		if envelope.Type != "queue_update" {
			return
		}
		payload, ok := envelope.Payload.(map[string]any)
		if !ok {
			t.Fatalf("unexpected queue payload type: %T", envelope.Payload)
		}
		event := queueEvent{
			Action:  payload["action"].(string),
			Pending: int(payload["pending"].(int)),
		}
		if kind, ok := payload["kind"].(string); ok {
			event.Kind = kind
		}
		if count, ok := payload["count"].(int); ok {
			event.Count = count
		}
		mu.Lock()
		seen = append(seen, event)
		mu.Unlock()
	})

	resultCh := make(chan types.RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := sess.Prompt(context.Background(), types.UserInput{Text: "start"}, types.PromptOptions{})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	time.Sleep(20 * time.Millisecond)
	if err := sess.FollowUp(context.Background(), types.UserInput{Text: "queued work"}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("prompt failed: %v", err)
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued run result")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected queue lifecycle events, got %+v", seen)
	}
	if seen[0].Action != "enqueued" || seen[0].Kind != "follow_up" || seen[0].Pending != 1 {
		t.Fatalf("unexpected enqueue event: %+v", seen[0])
	}
	if seen[1].Action != "drained" || seen[1].Count != 1 || seen[1].Pending != 0 {
		t.Fatalf("unexpected drain event: %+v", seen[1])
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type noSubagentRegistry struct {
	resolved capabilities.Resolved
}

func (r noSubagentRegistry) Resolve(context.Context, types.SessionScope, types.ModeRef) (capabilities.Resolved, error) {
	return r.resolved, nil
}

func testScope(root string) types.SessionScope {
	return types.SessionScope{
		Principal: types.PrincipalRef{ID: "principal-1"},
		Workspace: types.WorkspaceSpec{
			Ref:        types.WorkspaceRef{ID: "workspace-1"},
			Kind:       "local",
			WorkingDir: root,
		},
	}
}
