package main

import (
	"reflect"
	"testing"
)

func TestIndexBasicSearch(t *testing.T) {
	idx := NewIndex()
	idx.Add("go-intro", "Go is a statically typed compiled language designed at Google", nil)
	idx.Add("rust-intro", "Rust is a systems programming language focused on safety", nil)
	idx.Add("go-concurrency", "Go provides goroutines and channels for concurrent programming", nil)

	results := idx.Search("Go programming", 10)
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// "go-concurrency" mentions both "go" and "programming" so it should rank first.
	if results[0].ID != "go-concurrency" {
		t.Errorf("expected go-concurrency first, got %s", results[0].ID)
	}

	// All three docs should appear since "programming" or "go" appears in each.
	if len(results) < 2 {
		t.Errorf("expected at least 2 results, got %d", len(results))
	}
}

func TestIndexTagBoost(t *testing.T) {
	idx := NewIndex()
	idx.Add("content-only", "database migration tools are useful for schema changes", nil)
	idx.Add("tagged", "various development tools and utilities", []string{"database"})

	results := idx.Search("database", 10)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// The tagged doc should rank higher due to the 5x tag boost.
	if results[0].ID != "tagged" {
		t.Errorf("expected tagged doc first due to tag boost, got %s", results[0].ID)
	}
}

func TestIndexRemove(t *testing.T) {
	idx := NewIndex()
	idx.Add("keep", "important context about the project architecture", nil)
	idx.Add("remove-me", "temporary notes about architecture review", nil)

	idx.Remove("remove-me")

	results := idx.Search("architecture", 10)
	for _, r := range results {
		if r.ID == "remove-me" {
			t.Error("removed doc should not appear in search results")
		}
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "keep" {
		t.Errorf("expected 'keep', got %s", results[0].ID)
	}
}

func TestIndexIdentifierSplitting(t *testing.T) {
	idx := NewIndex()
	idx.Add("handler", "the handleCompact function processes compaction requests", nil)

	results := idx.Search("compact", 10)
	if len(results) == 0 {
		t.Fatal("expected to find doc via camelCase split, got none")
	}
	if results[0].ID != "handler" {
		t.Errorf("expected handler doc, got %s", results[0].ID)
	}
}

func TestIndexBM25LengthNormalization(t *testing.T) {
	idx := NewIndex()

	// Short doc with the target term.
	idx.Add("short", "compact server design", nil)

	// Long doc with the target term appearing only once, buried in filler.
	long := "the server architecture includes many components such as " +
		"authentication authorization logging monitoring caching routing " +
		"validation serialization deserialization middleware handlers " +
		"controllers services repositories models entities interfaces " +
		"adapters ports configuration deployment orchestration compact"
	idx.Add("long", long, nil)

	results := idx.Search("compact", 10)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Short doc should score higher due to BM25 length normalization.
	if results[0].ID != "short" {
		t.Errorf("expected short doc to rank first due to length normalization, got %s", results[0].ID)
	}
}

func TestIndexEmptySearch(t *testing.T) {
	idx := NewIndex()

	results := idx.Search("anything", 10)
	if len(results) != 0 {
		t.Errorf("expected empty results from empty index, got %d", len(results))
	}

	results = idx.Search("", 10)
	if len(results) != 0 {
		t.Errorf("expected empty results for empty query, got %d", len(results))
	}
}

func TestIndexMultipleTerms(t *testing.T) {
	idx := NewIndex()
	idx.Add("partial", "the server handles requests efficiently", nil)
	idx.Add("full-match", "the server handles context compaction efficiently", nil)

	results := idx.Search("context compaction server", 10)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// full-match has all three query terms; partial has only two.
	if results[0].ID != "full-match" {
		t.Errorf("expected full-match first (matches more query terms), got %s", results[0].ID)
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "camelCase",
			input: "handleCompact",
			want:  []string{"handle", "compact"},
		},
		{
			name:  "snake_case",
			input: "auto_compact",
			want:  []string{"auto", "compact"},
		},
		{
			name:  "mixed",
			input: "handleCompact auto_compact",
			want:  []string{"handle", "compact", "auto", "compact"},
		},
		{
			name:  "filters short tokens",
			input: "a I go do it",
			want:  []string{"go", "do", "it"},
		},
		{
			name:  "punctuation",
			input: "hello, world! foo-bar",
			want:  []string{"hello", "world", "foo", "bar"},
		},
		{
			name:  "uppercase",
			input: "HTTPServer",
			want:  []string{"httpserver"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
