// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

func TestCopilotPrompt(t *testing.T) {
	system, prompt, err := copilotPrompt([]Message{
		NewTextMessage("system", "Review the patch."),
		NewTextMessage("user", "Inspect file.go"),
	})
	if err != nil || system != "Review the patch." || prompt != "Inspect file.go" {
		t.Fatalf("initial prompt = (%q, %q, %v)", system, prompt, err)
	}

	_, prompt, err = copilotPrompt([]Message{
		NewTextMessage("system", "Review the patch."),
		NewTextMessage("user", "Inspect file.go"),
		NewToolCallMessage("", []ToolCall{{ID: "call-1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"file.go"}`}}}, NativeTurn{}, ""),
		NewToolResultMessage("call-1", "file contents"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var history []copilotReplayMessage
	if err := json.Unmarshal([]byte(prompt), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[1].ToolCalls[0].Function.Name != "read_file" || history[2].ToolCallID != "call-1" || history[2].Content != "file contents" {
		t.Fatalf("replay history = %#v", history)
	}
}

func TestCopilotRuntimePath(t *testing.T) {
	dir := t.TempDir()
	name := "copilot"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	installed := filepath.Join(dir, name)
	if err := os.WriteFile(installed, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("COPILOT_CLI_PATH", "")
	path, err := copilotRuntimePath("")
	if err != nil || path != installed {
		t.Fatalf("PATH runtime = (%q, %v), want %q", path, err, installed)
	}
	t.Setenv("COPILOT_CLI_PATH", "configured-runtime")
	path, err = copilotRuntimePath("")
	if err != nil || path != "configured-runtime" {
		t.Fatalf("configured runtime = (%q, %v)", path, err)
	}
	path, err = copilotRuntimePath("test-runtime")
	if err != nil || path != "test-runtime" {
		t.Fatalf("test runtime = (%q, %v)", path, err)
	}
}

func TestCopilotToolsRestrictSurface(t *testing.T) {
	defs := []ToolDef{{Type: "function", Function: FunctionDef{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object"}}}}
	tools, allowed, err := copilotTools(defs, "")
	if err != nil || len(tools) != 1 || tools[0].Handler != nil || !tools[0].OverridesBuiltInTool || tools[0].IsTerminal || len(allowed) != 1 || allowed[0] != "custom:read_file" {
		t.Fatalf("tools = (%#v, %#v, %v)", tools, allowed, err)
	}
	tools, allowed, err = copilotTools(defs, "none")
	if err != nil || len(tools) != 0 || len(allowed) != 0 || allowed == nil {
		t.Fatalf("no-tool surface = (%#v, %#v, %v)", tools, allowed, err)
	}
	if _, _, err := copilotTools(append(defs, defs[0]), ""); err == nil {
		t.Fatal("duplicate tool was accepted")
	}
}

func TestCopilotResponseRejectsUnavailableTool(t *testing.T) {
	message := &copilot.AssistantMessageData{ToolRequests: []copilot.AssistantMessageToolRequest{{ToolCallID: "call-1", Name: "shell", Arguments: map[string]any{"command": "echo hi"}}}}
	if _, err := copilotResponse(message, nil, "system", "prompt", nil, []string{"custom:read_file"}, "gpt-4.1"); err == nil || !strings.Contains(err.Error(), "unavailable tool") {
		t.Fatalf("unavailable tool error = %v", err)
	}
}

func TestCopilotResponseAccumulatesModelUsage(t *testing.T) {
	inputA, inputB := int64(10), int64(20)
	outputA, outputB := int64(2), int64(3)
	cacheA, cacheB := int64(4), int64(5)
	message := &copilot.AssistantMessageData{Content: "done"}
	response, err := copilotResponse(message, []*copilot.AssistantUsageData{
		{InputTokens: &inputA, OutputTokens: &outputA, CacheReadTokens: &cacheA},
		{InputTokens: &inputB, OutputTokens: &outputB, CacheReadTokens: &cacheB},
	}, "system", "prompt", nil, nil, "gpt-4.1")
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.PromptTokens != 30 || response.Usage.CompletionTokens != 5 || response.Usage.TotalTokens != 35 || response.Usage.CacheReadTokens != 9 {
		t.Fatalf("aggregated usage = %#v", response.Usage)
	}
}

func TestCopilotClientLocalToolBoundary(t *testing.T) {
	cliPath := os.Getenv("OCR_COPILOT_CLI_TEST_PATH")
	if cliPath == "" {
		t.Skip("set OCR_COPILOT_CLI_TEST_PATH to run the local Copilot CLI integration test")
	}

	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		call := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 4 {
			fmt.Fprint(w, `{"id":"probe-4","object":"chat.completion","created":4,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-probe-4a","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"a.go\"}"}},{"id":"call-probe-4b","type":"function","function":{"name":"code_comment","arguments":"{\"body\":\"check this\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18}}`)
			return
		}
		if call == 3 {
			fmt.Fprint(w, `{"id":"probe-3","object":"chat.completion","created":3,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"plan complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`)
			return
		}
		if call == 2 {
			fmt.Fprint(w, `{"id":"probe-2","object":"chat.completion","created":2,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-probe-2","type":"function","function":{"name":"task_done","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":3,"total_tokens":18}}`)
			return
		}
		fmt.Fprint(w, `{"id":"probe-1","object":"chat.completion","created":1,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-probe-1","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"file.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()

	client := &CopilotClient{model: "gpt-4.1", cliPath: cliPath, provider: &copilot.ProviderConfig{
		Type: "openai", BaseURL: server.URL + "/v1", APIKey: "local-probe", WireAPI: "completions",
	}}
	defer client.CloseSession("grace-test")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	response, err := client.CompletionsWithCtx(ctx, ChatRequest{
		SessionID: "grace-test",
		Messages:  []Message{NewTextMessage("system", "Call file_read once."), NewTextMessage("user", "Read file.go")},
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{Name: "file_read", Description: "Read a file", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := response.ToolCalls()
	if len(calls) != 1 || calls[0].Function.Name != "file_read" || calls[0].Function.Arguments != `{"path":"file.go"}` {
		t.Fatalf("tool calls = %#v", calls)
	}
	grace, err := client.CompletionsWithCtx(ctx, ChatRequest{
		SessionID: "grace-test",
		Messages: []Message{
			NewTextMessage("system", "Call file_read once."),
			NewTextMessage("user", "Read file.go"),
			NewToolCallMessage("", calls, NativeTurn{}, ""),
			NewToolResultMessage(calls[0].ID, "file contents"),
			NewTextMessage("user", "Final round. Call task_done."),
		},
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{Name: "task_done", Description: "Finish", Parameters: map[string]any{"type": "object"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := grace.ToolCalls(); len(got) != 1 || got[0].Function.Name != "task_done" {
		t.Fatalf("grace tool calls = %#v", got)
	}
	plain, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{
		NewTextMessage("system", "Answer directly."), NewTextMessage("user", "Finish the plan."),
	}})
	if err != nil || plain.VisibleContent() != "plan complete" || len(plain.ToolCalls()) != 0 {
		t.Fatalf("plain response = (%#v, %v)", plain, err)
	}
	parallel, err := client.CompletionsWithCtx(ctx, ChatRequest{
		Messages: []Message{NewTextMessage("system", "Use the supplied tools."), NewTextMessage("user", "Inspect a.go")},
		Tools: []ToolDef{
			{Type: "function", Function: FunctionDef{Name: "file_read", Parameters: map[string]any{"type": "object"}}},
			{Type: "function", Function: FunctionDef{Name: "code_comment", Parameters: map[string]any{"type": "object"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := parallel.ToolCalls(); len(got) != 2 || got[0].ID != "call-probe-4a" || got[1].ID != "call-probe-4b" {
		t.Fatalf("parallel tool calls = %#v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("model calls = %d, want one per OCR request", len(requests))
	}
	wireTools, ok := requests[0]["tools"].([]any)
	if !ok || len(wireTools) != 1 {
		t.Fatalf("model-facing tools = %#v", requests[0]["tools"])
	}
	graceTools, ok := requests[1]["tools"].([]any)
	if !ok || len(graceTools) != 1 {
		t.Fatalf("grace model-facing tools = %#v", requests[1]["tools"])
	}
	graceFunction := graceTools[0].(map[string]any)["function"].(map[string]any)
	if graceFunction["name"] != "task_done" {
		t.Fatalf("grace tool = %#v", graceFunction)
	}
	if plainTools, ok := requests[2]["tools"].([]any); ok && len(plainTools) != 0 {
		t.Fatalf("plain request unexpectedly exposed tools: %#v", plainTools)
	}
	if parallelTools, ok := requests[3]["tools"].([]any); !ok || len(parallelTools) != 2 {
		t.Fatalf("parallel model-facing tools = %#v", requests[3]["tools"])
	}
}

func TestCopilotClientNativeToolContinuation(t *testing.T) {
	cliPath := os.Getenv("OCR_COPILOT_CLI_TEST_PATH")
	if cliPath == "" {
		t.Skip("set OCR_COPILOT_CLI_TEST_PATH to run the local Copilot CLI integration test")
	}

	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		call := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			fmt.Fprint(w, `{"id":"native-1","object":"chat.completion","created":1,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-native-read","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"file.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			return
		}
		fmt.Fprint(w, `{"id":"native-2","object":"chat.completion","created":2,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-native-done","type":"function","function":{"name":"task_done","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":3,"total_tokens":18}}`)
	}))
	defer server.Close()

	client := &CopilotClient{model: "gpt-4.1", cliPath: cliPath, provider: &copilot.ProviderConfig{
		Type: "openai", BaseURL: server.URL + "/v1", APIKey: "local-probe", WireAPI: "completions",
	}}
	defer client.CloseSession("native-test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tools := []ToolDef{
		{Type: "function", Function: FunctionDef{Name: "file_read", Description: "Read a file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		{Type: "function", Function: FunctionDef{Name: "task_done", Description: "Finish", Parameters: map[string]any{"type": "object"}}},
	}
	start := []Message{NewTextMessage("system", "Use OCR tools."), NewTextMessage("user", "Inspect file.go and finish.")}
	first, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: start, Tools: tools, MaxTokens: 128, SessionID: "native-test"})
	if err != nil {
		t.Fatal(err)
	}
	calls := first.ToolCalls()
	if len(calls) != 1 || calls[0].Function.Name != "file_read" {
		t.Fatalf("first tool calls = %#v", calls)
	}
	continued := append(append([]Message(nil), start...),
		NewToolCallMessage("", calls, NativeTurn{}, ""),
		NewToolResultMessage(calls[0].ID, "file contents"))
	second, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: continued, Tools: tools, MaxTokens: 128, SessionID: "native-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.ToolCalls(); len(got) != 1 || got[0].Function.Name != "task_done" {
		t.Fatalf("second tool calls = %#v", got)
	}
	if _, kept := client.sessions.Load("native-test"); kept {
		t.Fatal("completed OCR session was not released")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(requests))
	}
	wireMessages, ok := requests[1]["messages"].([]any)
	if !ok {
		t.Fatalf("second model messages = %#v", requests[1]["messages"])
	}
	var sawToolResult bool
	for _, raw := range wireMessages {
		message, ok := raw.(map[string]any)
		if ok && message["role"] == "tool" && strings.Contains(fmt.Sprint(message["content"]), "file contents") {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("native tool result missing from second model request: %#v", wireMessages)
	}
}

func TestCopilotConversationFallsBackAfterCompression(t *testing.T) {
	start := []Message{NewTextMessage("system", "Review this patch."), NewTextMessage("user", "Inspect file.go")}
	conversation := &copilotConversation{
		session:   &copilot.Session{},
		model:     "gpt-4.1",
		system:    "Review this patch.",
		toolsKey:  "tools",
		maxTokens: 128,
		history:   start,
		pending:   map[string]string{"call-1": "request-1"},
	}
	call := ToolCall{ID: "call-1", Type: "function", Function: FunctionCall{Name: "file_read", Arguments: `{"path":"file.go"}`}}
	continued := append(append([]Message(nil), start...), NewToolCallMessage("", []ToolCall{call}, NativeTurn{}, ""), NewToolResultMessage("call-1", "file contents"))
	results, ok := conversation.continuation(ChatRequest{Messages: continued, MaxTokens: 128}, "gpt-4.1", "Review this patch.", "tools")
	if !ok || results["call-1"] != "file contents" {
		t.Fatalf("native continuation = (%#v, %v)", results, ok)
	}
	compressed := append([]Message(nil), continued...)
	compressed[1] = NewTextMessage("user", "Compressed summary")
	if _, ok := conversation.continuation(ChatRequest{Messages: compressed, MaxTokens: 128}, "gpt-4.1", "Review this patch.", "tools"); ok {
		t.Fatal("changed OCR transcript continued the stale SDK session")
	}
	if _, ok := conversation.continuation(ChatRequest{Messages: continued, MaxTokens: 128}, "gpt-4.1", "Review this patch.", "grace-tools"); ok {
		t.Fatal("changed OCR tool allowlist continued the stale SDK session")
	}
}

func TestCopilotClientOverridesBuiltInName(t *testing.T) {
	cliPath := os.Getenv("OCR_COPILOT_CLI_TEST_PATH")
	if cliPath == "" {
		t.Skip("set OCR_COPILOT_CLI_TEST_PATH to run the local Copilot CLI integration test")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"override-1","object":"chat.completion","created":1,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-override","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"file.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()
	client := &CopilotClient{model: "gpt-4.1", cliPath: cliPath, provider: &copilot.ProviderConfig{
		Type: "openai", BaseURL: server.URL + "/v1", APIKey: "local-probe", WireAPI: "completions",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := client.CompletionsWithCtx(ctx, ChatRequest{
		Messages: []Message{NewTextMessage("system", "Use the supplied tool."), NewTextMessage("user", "Read file.go")},
		Tools:    []ToolDef{{Type: "function", Function: FunctionDef{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls := response.ToolCalls(); len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Fatalf("tool calls = %#v", calls)
	}
}

func TestCopilotClientLocalCancellation(t *testing.T) {
	cliPath := os.Getenv("OCR_COPILOT_CLI_TEST_PATH")
	if cliPath == "" {
		t.Skip("set OCR_COPILOT_CLI_TEST_PATH to run the local Copilot CLI integration test")
	}
	received := make(chan struct{}, 1)
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		<-released
	}))
	defer server.Close()
	defer close(released)
	client := &CopilotClient{model: "gpt-4.1", cliPath: cliPath, provider: &copilot.ProviderConfig{
		Type: "openai", BaseURL: server.URL + "/v1", APIKey: "local-probe", WireAPI: "completions",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := client.CompletionsWithCtx(ctx, ChatRequest{Messages: []Message{
			NewTextMessage("system", "Answer directly."), NewTextMessage("user", "Wait for the response."),
		}})
		finished <- err
	}()
	select {
	case <-received:
		cancel()
	case <-ctx.Done():
		t.Fatal("Copilot CLI did not reach the local model")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled Copilot request returned no error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled Copilot request did not return promptly")
	}
}
