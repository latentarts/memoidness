package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/latentarts/memoidness/types"
)

var ErrToolArguments = errors.New("invalid tool arguments")
var ErrToolPolicy = errors.New("tool policy violation")

func Builtins() []Tool {
	return []Tool{
		ReadFileTool{},
		WriteFileTool{},
		ProcessTool{},
	}
}

type ReadFileTool struct{}

func (ReadFileTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: "read_file", Description: "Read a UTF-8 text file"}
}

func (ReadFileTool) Execute(ctx context.Context, call types.ToolCall, env Env) (types.ToolResult, error) {
	var args types.ToolReadFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return failure(call.ID, "tool_invalid_arguments", err.Error()), nil
	}
	path, err := resolvePath(env.WorkingDir, args.Path)
	if err != nil {
		return failure(call.ID, "tool_invalid_path", err.Error()), nil
	}
	if !allowedPath(path, env.Policy.Runtime.Filesystem.ReadableRoots) {
		return failure(call.ID, "tool_policy_read_denied", fmt.Sprintf("%v", ErrToolPolicy)), nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return failure(call.ID, "tool_read_failed", err.Error()), nil
	}
	payload, _ := json.Marshal(types.ToolReadFileResult{Path: path, Content: string(content)})
	return types.ToolResult{CallID: call.ID, Status: "ok", Payload: payload}, nil
}

type WriteFileTool struct{}

func (WriteFileTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: "write_file", Description: "Write or append UTF-8 text to a file"}
}

func (WriteFileTool) Execute(ctx context.Context, call types.ToolCall, env Env) (types.ToolResult, error) {
	var args types.ToolWriteFileArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return failure(call.ID, "tool_invalid_arguments", err.Error()), nil
	}
	path, err := resolvePath(env.WorkingDir, args.Path)
	if err != nil {
		return failure(call.ID, "tool_invalid_path", err.Error()), nil
	}
	if !allowedPath(path, env.Policy.Runtime.Filesystem.WritableRoots) {
		return failure(call.ID, "tool_policy_write_denied", fmt.Sprintf("%v", ErrToolPolicy)), nil
	}
	if err := ctx.Err(); err != nil {
		return types.ToolResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return failure(call.ID, "tool_write_failed", err.Error()), nil
	}
	flags := os.O_CREATE | os.O_WRONLY
	if args.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return failure(call.ID, "tool_write_failed", err.Error()), nil
	}
	defer file.Close()

	n, err := io.WriteString(file, args.Text)
	if err != nil {
		return failure(call.ID, "tool_write_failed", err.Error()), nil
	}
	payload, _ := json.Marshal(types.ToolWriteFileResult{
		Path:    path,
		Bytes:   n,
		Append:  args.Append,
		Written: true,
	})
	return types.ToolResult{CallID: call.ID, Status: "ok", Payload: payload}, nil
}

type ProcessTool struct{}

func (ProcessTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: "exec", Description: "Execute an allowed process and stream output"}
}

func (ProcessTool) Execute(ctx context.Context, call types.ToolCall, env Env) (types.ToolResult, error) {
	var args types.ToolExecArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return failure(call.ID, "tool_invalid_arguments", err.Error()), nil
	}
	if len(args.Command) == 0 {
		return failure(call.ID, "tool_invalid_arguments", "command is required"), nil
	}
	if !allowedCommand(args.Command, env.Policy.Runtime.Process.AllowedCommands) {
		return failure(call.ID, "tool_policy_command_denied", fmt.Sprintf("%v", ErrToolPolicy)), nil
	}

	cmd := exec.CommandContext(ctx, args.Command[0], args.Command[1:]...)
	cmd.Dir = env.WorkingDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return failure(call.ID, "tool_exec_failed", err.Error()), nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return failure(call.ID, "tool_exec_failed", err.Error()), nil
	}
	if err := cmd.Start(); err != nil {
		return failure(call.ID, "tool_exec_failed", err.Error()), nil
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go copyStream(&wg, call.ID, "stdout", stdout, &stdoutBuf, env.Emit)
	go copyStream(&wg, call.ID, "stderr", stderr, &stderrBuf, env.Emit)

	waitErr := cmd.Wait()
	wg.Wait()

	exitCode := cmd.ProcessState.ExitCode()
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		if env.Emit != nil {
			_ = env.Emit(types.ToolProgress{CallID: call.ID, ExitCode: &exitCode})
		}
		payload, _ := json.Marshal(types.ToolExecResult{
			Command:  args.Command,
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		})
		return types.ToolResult{
			CallID:  call.ID,
			Status:  "error",
			Payload: payload,
			Error: &types.Diagnostic{
				Severity: "error",
				Code:     "tool_exec_exit_nonzero",
				Message:  waitErr.Error(),
			},
		}, nil
	}
	if waitErr != nil {
		return types.ToolResult{}, waitErr
	}
	if env.Emit != nil {
		_ = env.Emit(types.ToolProgress{CallID: call.ID, ExitCode: &exitCode})
	}
	payload, _ := json.Marshal(types.ToolExecResult{
		Command:  args.Command,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	})
	return types.ToolResult{CallID: call.ID, Status: "ok", Payload: payload}, nil
}

func copyStream(wg *sync.WaitGroup, callID, stream string, src io.Reader, dst *bytes.Buffer, emit func(types.ToolProgress) error) {
	defer wg.Done()
	buf := make([]byte, 1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			dst.WriteString(text)
			if emit != nil {
				_ = emit(types.ToolProgress{CallID: callID, Stream: stream, Text: text})
			}
		}
		if err != nil {
			return
		}
	}
}

func allowedPath(path string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func allowedCommand(command, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	joined := strings.Join(command, " ")
	for _, prefix := range allowed {
		if prefix != "" && (joined == prefix || strings.HasPrefix(joined, prefix+" ")) {
			return true
		}
	}
	return false
}

func resolvePath(workingDir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: path is required", ErrToolArguments)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(filepath.Join(workingDir, path))
}

func failure(callID, code, message string) types.ToolResult {
	return types.ToolResult{
		CallID: callID,
		Status: "error",
		Error: &types.Diagnostic{
			Severity: "error",
			Code:     code,
			Message:  message,
		},
	}
}
