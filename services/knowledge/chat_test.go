package main

import (
	"strings"
	"testing"

	"github.com/atlas/platform/pkg/llmclient"
)

func TestBuildChatPromptRAG(t *testing.T) {
	sys, user := buildChatPrompt("How is mail sent?", []hit{
		{Title: "On-call", Content: "SES delivers reminder mail."},
	}, true)
	if !strings.Contains(sys, "retrieval-augmented") {
		t.Fatalf("system: %s", sys)
	}
	if !strings.Contains(user, "Sources:") || !strings.Contains(user, "Question:\nHow is mail sent?") {
		t.Fatalf("user: %s", user)
	}
}

func TestBuildChatPromptPlain(t *testing.T) {
	sys, user := buildChatPrompt("Hello", nil, false)
	if strings.Contains(sys, "retrieval-augmented") {
		t.Fatalf("plain chat should not use RAG system prompt: %s", sys)
	}
	if user != "Hello" {
		t.Fatalf("user %q", user)
	}
}

func TestMergeHitsDedupAndLimit(t *testing.T) {
	a := []hit{{DocumentID: "1", Title: "A", Content: "alpha", Distance: 0.4}}
	b := []hit{
		{DocumentID: "1", Title: "A", Content: "alpha", Distance: 0.1},
		{DocumentID: "2", Title: "B", Content: "beta", Distance: 0.2},
		{DocumentID: "3", Title: "C", Content: "gamma", Distance: 0.9},
	}
	got := mergeHits(a, b, 2)
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	if got[0].Title != "A" || got[0].Distance != 0.1 || got[1].Title != "B" {
		t.Fatalf("order %+v", got)
	}
}

func TestCosineDistance(t *testing.T) {
	same := cosineDistance([]float32{1, 0}, []float32{1, 0})
	if same > 1e-6 {
		t.Fatalf("identical vectors: %f", same)
	}
	orth := cosineDistance([]float32{1, 0}, []float32{0, 1})
	if orth < 0.99 {
		t.Fatalf("orthogonal: %f", orth)
	}
}

func TestSanitizeHistory(t *testing.T) {
	in := []llmclient.Message{
		{Role: "system", Content: "nope"},
		{Role: "user", Content: "  hi  "},
		{Role: "assistant", Content: ""},
		{Role: "assistant", Content: "hello"},
	}
	got := sanitizeHistory(in)
	if len(got) != 3 {
		t.Fatalf("len %d %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "nope" {
		t.Fatalf("system should map to user: %+v", got[0])
	}
	if got[1].Content != "hi" || got[2].Role != "assistant" {
		t.Fatalf("%+v", got)
	}
}

func TestSanitizeHistoryCaps(t *testing.T) {
	var in []llmclient.Message
	for i := 0; i < 20; i++ {
		in = append(in, llmclient.Message{Role: "user", Content: strings.Repeat("x", 1) + itoa(i)})
	}
	got := sanitizeHistory(in)
	if len(got) != maxChatHistory {
		t.Fatalf("len %d", len(got))
	}
	if got[len(got)-1].Content != "x19" {
		t.Fatalf("should keep the tail, last=%s", got[len(got)-1].Content)
	}
}
