package main

import "testing"

func TestLocalFAQRetrieval(t *testing.T) {
	got := localComplete("You are Atlas", "How does retrieval work?")
	if !containsAny(got, "pgvector") {
		t.Fatalf("expected retrieval answer, got %q", got)
	}
}

func TestLocalRAGUsesSources(t *testing.T) {
	prompt := `Sources:
Source 1 (On-call):
Page the on-call via SES when a reminder fires.

Question:
How are reminders delivered?`
	got := localComplete("You are Atlas, a retrieval-augmented assistant.", prompt)
	if !containsAny(got, "SES") {
		t.Fatalf("expected source content, got %q", got)
	}
}

func TestLocalRetrievedContext(t *testing.T) {
	system := "You are Atlas chat.\n\nRetrieved context:\n- Onboarding notes: We promised legal a written summary of onboarding risks.\n"
	got := localComplete(system, "What did we promise legal?")
	if !containsAny(got, "Onboarding notes", "written summary") {
		t.Fatalf("expected the stored note, got %q", got)
	}
}

func TestLocalAgentUsesNotes(t *testing.T) {
	system := "You are Atlas agent.\n\nRetrieved context:\n- Onboarding notes: We promised legal a written summary.\n"
	got := localComplete(system, "What did we promise legal?")
	if !containsAny(got, "1. Read the matching note", "written summary") {
		t.Fatalf("expected agent steps, got %q", got)
	}
}

func TestLocalRAGEmptySources(t *testing.T) {
	prompt := "Sources:\n\nQuestion:\nWhat did I store?"
	got := localComplete("rag", prompt)
	if !containsAny(got, "Ingest") && !containsAny(got, "ingest") && !containsAny(got, "don't have") {
		t.Fatalf("expected empty-corpus hint, got %q", got)
	}
}
