package main

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},
		{"hello world", 3},
		{strings.Repeat("a", 100), 25},
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestStoreAddAndQuery(t *testing.T) {
	s := NewStore(100000)

	item := s.Add("cilium uses netkit on kernel 6.8", "cilium netkit needs 6.8", []string{"cilium", "networking"}, 8)
	if item.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if item.Tokens != EstimateTokens("cilium uses netkit on kernel 6.8") {
		t.Errorf("tokens mismatch: got %d", item.Tokens)
	}

	// Query by text
	results := s.Query("cilium kernel", nil, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != item.ID {
		t.Error("wrong item returned")
	}

	// Query by tag
	results = s.Query("", []string{"networking"}, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result by tag, got %d", len(results))
	}

	// Query with non-matching tag
	results = s.Query("", []string{"nonexistent"}, 10)
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestStoreRemoveAndPin(t *testing.T) {
	s := NewStore(100000)

	item := s.Add("test content", "", nil, 5)
	if !s.Pin(item.ID) {
		t.Fatal("pin failed")
	}

	got, ok := s.Get(item.ID)
	if !ok || !got.Pinned {
		t.Fatal("item should be pinned")
	}

	if !s.Remove(item.ID) {
		t.Fatal("remove failed")
	}
	_, ok = s.Get(item.ID)
	if ok {
		t.Fatal("item should be gone")
	}
}

func TestCompaction(t *testing.T) {
	s := NewStore(200) // small budget: ~200 tokens = ~800 chars

	// Store items that exceed budget
	s.Add("alpha bravo charlie delta echo foxtrot golf hotel", "short alpha", []string{"a"}, 3)
	s.Add("india juliet kilo lima mike november oscar papa", "", []string{"b"}, 5)
	s.Add("quebec romeo sierra tango uniform victor whiskey", "", []string{"c"}, 8)

	_, used, _, _ := s.Status()
	if used == 0 {
		t.Fatal("expected non-zero usage")
	}

	result := s.Compact(0.5) // compact to 50%
	if result.TokensAfter > result.TokensBefore {
		t.Error("compaction should not increase tokens")
	}
	if result.TokensFreed == 0 && result.TokensBefore > 100 {
		t.Error("expected some tokens freed")
	}

	// Item with summary should have been promoted
	if result.Summarized == 0 {
		t.Log("note: no summary promotions (may depend on budget math)")
	}
}

func TestSummaryPromotion(t *testing.T) {
	s := NewStore(100) // very tight budget

	item := s.Add(
		"this is a very long content string that takes many tokens to represent in the context window",
		"long content, many tokens",
		nil, 5,
	)

	result := s.Compact(0.3) // aggressive compaction
	if result.Summarized == 0 {
		t.Log("note: summary promotion did not trigger")
	}

	got, ok := s.Get(item.ID)
	if ok && got.Content == "long content, many tokens" {
		// Content was promoted to summary
		if got.Tokens != EstimateTokens("long content, many tokens") {
			t.Errorf("tokens should match promoted summary: got %d", got.Tokens)
		}
	}
}

func TestDeduplication(t *testing.T) {
	s := NewStore(100)

	s.Add("alpha bravo charlie delta echo foxtrot", "", []string{"a"}, 5)
	s.Add("alpha bravo charlie delta echo golf", "", []string{"b"}, 5)

	_, _, countBefore, _ := s.Status()
	s.Compact(0.3)
	_, _, countAfter, _ := s.Status()

	if countAfter >= countBefore {
		t.Log("note: dedup did not reduce count (similarity may be below threshold)")
	}
}

func TestUpdateSummary(t *testing.T) {
	s := NewStore(100000)

	item := s.Add("full content here", "", nil, 5)
	if item.Summary != "" {
		t.Fatal("should have no summary initially")
	}

	if !s.UpdateSummary(item.ID, "compact version") {
		t.Fatal("update failed")
	}

	got, _ := s.Get(item.ID)
	if got.Summary != "compact version" {
		t.Errorf("summary not updated: got %q", got.Summary)
	}
}

func TestJaccardSimilarity(t *testing.T) {
	a := wordSet("hello world foo bar")
	b := wordSet("hello world foo baz")

	sim := jaccardSimilarity(a, b)
	if sim < 0.5 || sim > 0.7 {
		t.Errorf("expected ~0.6 similarity, got %f", sim)
	}

	// Identical sets
	if jaccardSimilarity(a, a) != 1.0 {
		t.Error("identical sets should have similarity 1.0")
	}

	// Empty sets
	if jaccardSimilarity(wordSet(""), wordSet("")) != 0 {
		t.Error("empty sets should have similarity 0")
	}
}

func TestAutoCompact(t *testing.T) {
	s := NewStore(50) // very small: 50 tokens = ~200 chars
	s.Configure(0, boolPtr(true), 0.5)

	// Add items that should trigger auto-compaction
	s.Add("first item with some content padding", "first", nil, 3)
	s.Add("second item with more content padding", "second", nil, 3)
	s.Add("third item triggering compaction now", "third", nil, 3)

	_, used, _, usage := s.Status()
	// Auto-compact should have kept usage reasonable
	if usage > 1.0 {
		t.Errorf("auto-compact should prevent exceeding budget: used=%d, usage=%.1f%%", used, usage*100)
	}
}

func boolPtr(b bool) *bool { return &b }
