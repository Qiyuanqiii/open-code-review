// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
)

type behaviorCompressionClient struct {
	mu           sync.Mutex
	reports      []BehaviorReport
	stage        int
	requests     [3]int
	original     [3]string
	compressions int
	missing      []string
}

func (c *behaviorCompressionClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(req.Tools) == 0 {
		c.compressions++
		// Deliberately omit every contract, source and check from the summary.
		text := "Earlier file reads completed."
		return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: &text}}}}, nil
	}
	if c.stage >= len(c.reports) {
		return nil, fmt.Errorf("unexpected review request")
	}
	c.requests[c.stage]++
	var previous string
	var policy bool
	for _, message := range req.Messages {
		text := message.ExtractText()
		policy = policy || text == behaviorPrompt
		if _, payload, ok := strings.Cut(text, "Previous stage records (claims to verify):\n"); ok {
			previous = payload
		}
	}
	if c.requests[c.stage] == 1 {
		c.original[c.stage] = previous
	}
	if !policy || previous == "" || previous != c.original[c.stage] {
		c.missing = append(c.missing, fmt.Sprintf("stage %s request %d lost its policy or original records", behaviorStages[c.stage], c.requests[c.stage]))
	}
	response := agentTaskDoneResponse()
	if c.requests[c.stage] < 3 {
		// Exceed the compression warning threshold twice in every stage.
		text := strings.Repeat("context ", 30000)
		response.Choices[0].Message.Content = &text
		response.Choices[0].Message.ToolCalls[0].Function = llm.FunctionCall{Name: "file_read", Arguments: `{"path":"good.go"}`}
		return response, nil
	}
	data, err := json.Marshal(reportArgs(c.reports[c.stage]))
	response.Choices[0].Message.ToolCalls[0].Function.Arguments = string(data)
	c.stage++
	return response, err
}

func TestBehaviorInputsSurviveRepeatedCompression(t *testing.T) {
	for _, systemPrompt := range []bool{false, true} {
		t.Run(fmt.Sprintf("system_prompt=%v", systemPrompt), func(t *testing.T) {
			client := &behaviorCompressionClient{reports: behaviorFixtures()}
			client.reports[0].Unknowns = []string{"Trace the caller's zero-value behavior in the next stage."}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			if systemPrompt {
				a.args.Template.MainTask.Messages = append([]template.ChatMessage{{Role: "system", Content: "Review code."}}, a.args.Template.MainTask.Messages...)
			}
			a.args.Template.MaxTokens = 20000
			a.args.Template.MemoryCompressionTask.Messages = []template.ChatMessage{{Role: "user", Content: "Summarize: {{context}}"}}
			a.args.MainToolDefs = append(a.args.MainToolDefs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: "file_read"}})
			a.args.Tools.Register(behaviorBudgetRead{})
			configureCompletionAgent(a, 8)
			_, _ = a.dispatchSubtasks(context.Background())
			a.runner.WaitBackground()
			manifest := finishManifestFlow(t, a)
			if manifest.TerminalState != session.StateComplete || client.requests != [3]int{3, 3, 3} || client.compressions != 6 {
				t.Fatalf("compression path not exercised: state=%s requests=%v compressions=%d", manifest.TerminalState, client.requests, client.compressions)
			}
			if len(client.missing) != 0 {
				t.Fatal(strings.Join(client.missing, "; "))
			}
		})
	}
}
