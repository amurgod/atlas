package main

import "testing"

func TestChunkTextKeepsParagraphs(t *testing.T) {
	parts := chunkText("alpha\n\nbeta", 900)
	if len(parts) != 1 || parts[0] != "alpha\nbeta" {
		t.Fatalf("short paragraphs should pack, got %#v", parts)
	}
}

func TestChunkTextSplitsLongRun(t *testing.T) {
	long := ""
	for i := 0; i < 50; i++ {
		long += "word "
	}
	parts := chunkText(long, 40)
	if len(parts) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(parts))
	}
	for _, p := range parts {
		if len(p) > 80 {
			t.Fatalf("chunk too large: %d", len(p))
		}
	}
}
