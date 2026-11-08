package main

import "testing"

func TestBagOfWordsNormalized(t *testing.T) {
	a := bagOfWords("postgres pgvector retrieval chunks", 1024)
	b := bagOfWords("postgres pgvector retrieval chunks", 1024)
	c := bagOfWords("unrelated reminder twilio sms", 1024)
	if cosine(a, b) < 0.99 {
		t.Fatalf("identical text should match, got %f", cosine(a, b))
	}
	if cosine(a, c) >= cosine(a, b) {
		t.Fatalf("unrelated text should score lower")
	}
}

func cosine(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
