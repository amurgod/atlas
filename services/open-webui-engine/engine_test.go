package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestChunkText(t *testing.T) {
	parts := chunkText("alpha\n\nbeta\n\ngamma", 8)
	if len(parts) < 2 {
		t.Fatalf("expected multiple chunks, got %#v", parts)
	}
}

func TestMemoryStoreAndVectors(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	if err := store.Put(ctx, "u/d.txt", "text/plain", []byte("hello docs")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "u/d.txt")
	if err != nil || string(got) != "hello docs" {
		t.Fatalf("get: %s %v", got, err)
	}
	idx := newMemIndex(nil)
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	chunks := []storedChunk{{
		ID:         uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		DocumentID: id,
		UserID:     "user-1",
		Content:    "kubernetes pods run containers",
		Embedding:  []float32{1, 0, 0},
		Title:      "k8s",
	}, {
		ID:         uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		DocumentID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		UserID:     "user-1",
		Content:    "baking bread needs yeast",
		Embedding:  []float32{0, 1, 0},
		Title:      "bread",
	}}
	if err := idx.Upsert(ctx, chunks); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(ctx, "user-1", []float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "k8s" {
		t.Fatalf("hits %#v", hits)
	}
	if err := idx.DeleteDocument(ctx, "user-1", id.String()); err != nil {
		t.Fatal(err)
	}
	hits, err = idx.Search(ctx, "user-1", []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "bread" {
		t.Fatalf("after delete %#v", hits)
	}
	if err := store.Delete(ctx, "u/d.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestCosineDistance(t *testing.T) {
	if d := cosineDistance([]float32{1, 0}, []float32{1, 0}); d > 0.001 {
		t.Fatalf("identical vectors dist=%f", d)
	}
	if d := cosineDistance([]float32{1, 0}, []float32{0, 1}); d < 0.9 {
		t.Fatalf("orthogonal dist=%f", d)
	}
}
