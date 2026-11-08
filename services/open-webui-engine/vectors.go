package main

import (
	"context"
	"math"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type Hit struct {
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Distance   float64 `json:"distance"`
}

type storedChunk struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	UserID     string
	Ordinal    int
	Content    string
	Embedding  []float32
	Title      string
}

type VectorIndex interface {
	Kind() string
	Upsert(ctx context.Context, chunks []storedChunk) error
	DeleteDocument(ctx context.Context, userID, docID string) error
	Search(ctx context.Context, userID string, embedding []float32, limit int) ([]Hit, error)
	Load(ctx context.Context) error
}

type pgIndex struct {
	pool *pgxpool.Pool
}

func (p *pgIndex) Kind() string { return "pgvector" }

func (p *pgIndex) Load(context.Context) error { return nil }

func (p *pgIndex) Upsert(ctx context.Context, chunks []storedChunk) error {
	for _, c := range chunks {
		_, err := p.pool.Exec(ctx, `
			INSERT INTO engine_chunks (id, document_id, user_id, ordinal, content, embedding)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			c.ID, c.DocumentID, c.UserID, c.Ordinal, c.Content, pgvector.NewVector(c.Embedding))
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *pgIndex) DeleteDocument(ctx context.Context, userID, docID string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM engine_chunks WHERE document_id=$1 AND user_id=$2`, docID, userID)
	return err
}

func (p *pgIndex) Search(ctx context.Context, userID string, embedding []float32, limit int) ([]Hit, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT d.id::text, d.title, c.content, (c.embedding <=> $2) AS dist
		FROM engine_chunks c
		JOIN engine_documents d ON d.id = c.document_id
		WHERE c.user_id = $1
		ORDER BY c.embedding <=> $2
		LIMIT $3`,
		userID, pgvector.NewVector(embedding), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.DocumentID, &h.Title, &h.Content, &h.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

type memIndex struct {
	mu     sync.RWMutex
	chunks []storedChunk
	inner  VectorIndex
}

func newMemIndex(persist VectorIndex) *memIndex {
	return &memIndex{inner: persist, chunks: []storedChunk{}}
}

func (m *memIndex) Kind() string { return "memory" }

func (m *memIndex) Load(ctx context.Context) error {
	if loader, ok := m.inner.(interface {
		loadAll(context.Context) ([]storedChunk, error)
	}); ok {
		chunks, err := loader.loadAll(ctx)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.chunks = chunks
		m.mu.Unlock()
	}
	return nil
}

func (p *pgIndex) loadAll(ctx context.Context) ([]storedChunk, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT c.id, c.document_id, c.user_id::text, c.ordinal, c.content, c.embedding, d.title
		FROM engine_chunks c
		JOIN engine_documents d ON d.id = c.document_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedChunk
	for rows.Next() {
		var c storedChunk
		var vec pgvector.Vector
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.UserID, &c.Ordinal, &c.Content, &vec, &c.Title); err != nil {
			return nil, err
		}
		c.Embedding = vec.Slice()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *memIndex) Upsert(ctx context.Context, chunks []storedChunk) error {
	if m.inner != nil {
		if err := m.inner.Upsert(ctx, chunks); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = append(m.chunks, chunks...)
	return nil
}

func (m *memIndex) DeleteDocument(ctx context.Context, userID, docID string) error {
	if m.inner != nil {
		if err := m.inner.DeleteDocument(ctx, userID, docID); err != nil {
			return err
		}
	}
	id, err := uuid.Parse(docID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.chunks[:0]
	for _, c := range m.chunks {
		if c.DocumentID == id && c.UserID == userID {
			continue
		}
		kept = append(kept, c)
	}
	m.chunks = kept
	return nil
}

func (m *memIndex) Search(_ context.Context, userID string, embedding []float32, limit int) ([]Hit, error) {
	type scored struct {
		hit Hit
		d   float64
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ranked []scored
	for _, c := range m.chunks {
		if c.UserID != userID {
			continue
		}
		d := cosineDistance(embedding, c.Embedding)
		ranked = append(ranked, scored{hit: Hit{
			DocumentID: c.DocumentID.String(),
			Title:      c.Title,
			Content:    c.Content,
			Distance:   d,
		}, d: d})
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].d < ranked[i].d {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	if limit <= 0 || limit > len(ranked) {
		limit = len(ranked)
	}
	out := make([]Hit, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, ranked[i].hit)
	}
	return out, nil
}

func cosineDistance(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 1
	}
	sim := dot / (math.Sqrt(na) * math.Sqrt(nb))
	return 1 - sim
}

func newVectorIndex(backend string, pool *pgxpool.Pool) VectorIndex {
	pg := &pgIndex{pool: pool}
	if backend == "memory" {
		return newMemIndex(pg)
	}
	return pg
}

