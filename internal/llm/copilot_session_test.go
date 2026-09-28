// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

func TestCopilotConversationWaitCollectsChunksAndParallelTools(t *testing.T) {
	chunkCount, firstChunk, lastChunk := int64(2), int64(0), int64(1)
	input, output := int64(23), int64(7)
	usage := &copilot.AssistantUsageData{InputTokens: &input, OutputTokens: &output}
	events := []copilot.SessionEvent{
		{Data: &copilot.ExternalToolRequestedData{ToolCallID: "read-1", RequestID: "request-1"}},
		{Data: &copilot.AssistantMessageData{
			MessageID: "message-1", Content: "Inspect ", ChunkCount: &chunkCount, ChunkIndex: &firstChunk,
			ToolRequests: []copilot.AssistantMessageToolRequest{{ToolCallID: "read-1", Name: "file_read"}},
		}},
		{Data: usage},
		{Data: &copilot.AssistantMessageData{
			MessageID: "message-1", Content: "files.", ChunkCount: &chunkCount, ChunkIndex: &lastChunk, OutputTokens: &output,
			ToolRequests: []copilot.AssistantMessageToolRequest{{ToolCallID: "read-2", Name: "file_read"}},
		}},
		{Data: &copilot.ExternalToolRequestedData{ToolCallID: "read-2", RequestID: "request-2"}},
	}
	conversation := &copilotConversation{events: make(chan copilot.SessionEvent, len(events))}
	for _, event := range events {
		conversation.events <- event
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message, usages, pending, err := conversation.wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "Inspect files." || len(message.ToolRequests) != 2 || message.OutputTokens == nil || *message.OutputTokens != output {
		t.Fatalf("assembled message = %#v", message)
	}
	if len(usages) != 1 || usages[0] != usage {
		t.Fatalf("usage events = %#v", usages)
	}
	if !reflect.DeepEqual(pending, map[string]string{"read-1": "request-1", "read-2": "request-2"}) {
		t.Fatalf("pending OCR tools = %#v", pending)
	}
	if len(conversation.events) != 0 {
		t.Fatal("returned before all assistant chunks and external tools arrived")
	}
}

func TestCopilotConversationWaitForIdleWithoutTools(t *testing.T) {
	conversation := &copilotConversation{events: make(chan copilot.SessionEvent, 2)}
	conversation.events <- copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "Review complete."}}
	conversation.events <- copilot.SessionEvent{Data: &copilot.SessionIdleData{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message, _, pending, err := conversation.wait(ctx)
	if err != nil || message == nil || message.Content != "Review complete." || len(pending) != 0 {
		t.Fatalf("plain response = (%#v, %#v, %v)", message, pending, err)
	}
}

func TestCopilotConversationWaitRejectsInvalidEvents(t *testing.T) {
	request := copilot.SessionEvent{Data: &copilot.AssistantMessageData{
		MessageID: "message-1", ToolRequests: []copilot.AssistantMessageToolRequest{{ToolCallID: "read-1", Name: "file_read"}},
	}}
	cases := []struct {
		name   string
		events []copilot.SessionEvent
		want   string
	}{
		{name: "idle without message", events: []copilot.SessionEvent{{Data: &copilot.SessionIdleData{}}}, want: "without an assistant message"},
		{name: "missing pending request", events: []copilot.SessionEvent{request, {Data: &copilot.SessionIdleData{}}}, want: "without all pending"},
		{name: "missing tool ID", events: []copilot.SessionEvent{{Data: &copilot.ExternalToolRequestedData{RequestID: "request-1"}}}, want: "invalid pending"},
		{name: "missing request ID", events: []copilot.SessionEvent{{Data: &copilot.ExternalToolRequestedData{ToolCallID: "read-1"}}}, want: "invalid pending"},
		{name: "unannounced tool", events: []copilot.SessionEvent{request,
			{Data: &copilot.ExternalToolRequestedData{ToolCallID: "shell-1", RequestID: "request-1"}},
		}, want: "unannounced tool call"},
		{name: "advanced past OCR tool", events: []copilot.SessionEvent{request,
			{Data: &copilot.AssistantMessageData{MessageID: "message-2", Content: "Continued without OCR."}},
		}, want: "advanced past an OCR tool request"},
		{name: "upstream error", events: []copilot.SessionEvent{{Data: &copilot.SessionErrorData{Message: "authentication failed"}}}, want: "authentication failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conversation := &copilotConversation{events: make(chan copilot.SessionEvent, len(tc.events))}
			for _, event := range tc.events {
				conversation.events <- event
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			message, usages, pending, err := conversation.wait(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) || message != nil || usages != nil || pending != nil {
				t.Fatalf("invalid event result = (%#v, %#v, %#v, %v), want %q", message, usages, pending, err, tc.want)
			}
		})
	}
}

func TestCopilotConversationWaitCancellationAndOverflow(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		conversation := &copilotConversation{}
		if _, _, _, err := conversation.wait(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled wait = %v", err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		conversation := &copilotConversation{overflow: make(chan struct{}, 1)}
		conversation.overflow <- struct{}{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, _, _, err := conversation.wait(ctx); err == nil || !strings.Contains(err.Error(), "buffer overflowed") {
			t.Fatalf("overflowed wait = %v", err)
		}
	})
}

func TestCopilotConversationContinuationRequiresAllToolResults(t *testing.T) {
	start := []Message{NewTextMessage("system", "Review the patch."), NewTextMessage("user", "Inspect files.")}
	calls := []ToolCall{
		{ID: "read-1", Type: "function", Function: FunctionCall{Name: "file_read", Arguments: `{"path":"a.go"}`}},
		{ID: "read-2", Type: "function", Function: FunctionCall{Name: "file_read", Arguments: `{"path":"b.go"}`}},
	}
	continued := append(append([]Message(nil), start...), NewToolCallMessage("", calls, NativeTurn{}, ""),
		NewToolResultMessage("read-2", "file b"), NewToolResultMessage("read-1", "file a"))
	newConversation := func() *copilotConversation {
		return &copilotConversation{
			session: &copilot.Session{}, model: "auto", system: "Review the patch.", toolsKey: "tools", maxTokens: 128,
			history: start, pending: map[string]string{"read-1": "request-1", "read-2": "request-2"},
		}
	}
	req := ChatRequest{Messages: continued, MaxTokens: 128}
	results, ok := newConversation().continuation(req, "auto", "Review the patch.", "tools")
	if !ok || !reflect.DeepEqual(results, map[string]string{"read-1": "file a", "read-2": "file b"}) {
		t.Fatalf("parallel continuation = (%#v, %t)", results, ok)
	}
	cases := []struct {
		name   string
		change func(*copilotConversation, *ChatRequest)
	}{
		{name: "missing result", change: func(_ *copilotConversation, req *ChatRequest) { req.Messages = continued[:len(continued)-1] }},
		{name: "unknown result", change: func(_ *copilotConversation, req *ChatRequest) {
			req.Messages = append(append([]Message(nil), continued...), NewToolResultMessage("unknown", "unexpected"))
		}},
		{name: "no pending tools", change: func(c *copilotConversation, _ *ChatRequest) { c.pending = nil }},
		{name: "no new turn", change: func(_ *copilotConversation, req *ChatRequest) { req.Messages = start }},
		{name: "model changed", change: func(c *copilotConversation, _ *ChatRequest) { c.model = "another-model" }},
		{name: "system changed", change: func(c *copilotConversation, _ *ChatRequest) { c.system = "Another review." }},
		{name: "limit changed", change: func(_ *copilotConversation, req *ChatRequest) { req.MaxTokens = 64 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conversation := newConversation()
			changed := req
			tc.change(conversation, &changed)
			if _, ok := conversation.continuation(changed, "auto", "Review the patch.", "tools"); ok {
				t.Fatal("continued a stale or incomplete SDK conversation")
			}
		})
	}
}

func TestCopilotCloseSessionReleasesState(t *testing.T) {
	stateDir := t.TempDir()
	unsubscribed := 0
	conversation := &copilotConversation{
		stateDir: stateDir, unsubscribe: func() { unsubscribed++ },
		events: make(chan copilot.SessionEvent, 1), overflow: make(chan struct{}, 1),
		history: []Message{NewTextMessage("user", "Inspect files.")}, pending: map[string]string{"read-1": "request-1"},
	}
	client := NewCopilotClient(ClientConfig{Model: "auto"})
	client.sessions.Store("review", conversation)
	client.CloseSession("")
	client.CloseSession("missing")
	if _, kept := client.sessions.Load("review"); !kept {
		t.Fatal("closing an unrelated session removed the active review")
	}
	client.CloseSession("review")
	client.CloseSession("review")
	if _, kept := client.sessions.Load("review"); kept || unsubscribed != 1 {
		t.Fatalf("session retained = %t, unsubscribe calls = %d", kept, unsubscribed)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory remains: %v", err)
	}
	if conversation.events != nil || conversation.overflow != nil || conversation.history != nil || conversation.pending != nil {
		t.Fatal("closed session retained conversation state")
	}
}

func TestCopilotDiscardConversationKeepsReplacement(t *testing.T) {
	client := NewCopilotClient(ClientConfig{Model: "auto"})
	old, replacement := &copilotConversation{}, &copilotConversation{}
	client.sessions.Store("review", replacement)
	client.discardConversation("review", old)
	if current, ok := client.sessions.Load("review"); !ok || current != replacement {
		t.Fatal("discarding stale state removed the replacement session")
	}
	client.discardConversation("review", replacement)
	if _, ok := client.sessions.Load("review"); ok {
		t.Fatal("discarded conversation remains registered")
	}
}
