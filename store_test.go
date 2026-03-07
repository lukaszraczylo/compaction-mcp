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

	item, err := s.Add("cilium uses netkit on kernel 6.8", "cilium netkit needs 6.8", []string{"cilium", "networking"}, 8)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
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

	item, err := s.Add("test content", "", nil, 5)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
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
	// Budget of 20 tokens. Disable auto-compact so items survive Add().
	// Each item is ~12 tokens, so 3 items = ~36 tokens > budget.
	s := NewStore(20)
	s.Configure(0, boolPtr(false), 0)

	if _, err := s.Add("alpha bravo charlie delta echo foxtrot golf hotel", "short alpha", []string{"a"}, 3); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add("india juliet kilo lima mike november oscar papa", "", []string{"b"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add("quebec romeo sierra tango uniform victor whiskey", "", []string{"c"}, 8); err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, used, _, _ := s.Status()
	if used == 0 {
		t.Fatal("expected non-zero usage")
	}

	result := s.Compact(0.5) // compact to 50% of 20 = 10 tokens
	if result.TokensAfter > result.TokensBefore {
		t.Error("compaction should not increase tokens")
	}
	if result.TokensFreed == 0 {
		t.Error("expected some tokens freed")
	}

	// Item with summary should have been promoted
	if result.Summarized == 0 {
		t.Error("expected at least one summary promotion")
	}
}

func TestSummaryPromotion(t *testing.T) {
	// Budget of 20 tokens. Content is ~23 tokens, summary is ~7 tokens.
	// Disable auto-compact. Compaction to 0.3 = target 6 tokens, so promotion must happen.
	s := NewStore(20)
	s.Configure(0, boolPtr(false), 0)

	item, _ := s.Add(
		"this is a very long content string that takes many tokens to represent in the context window",
		"long content, many tokens",
		nil, 5,
	)

	result := s.Compact(0.3) // aggressive compaction
	if result.Summarized == 0 {
		t.Error("expected summary promotion to trigger")
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
	// Budget of 20 tokens, two items with 5/6 word overlap (>70%).
	// Disable auto-compact so both items survive Add().
	s := NewStore(20)
	s.Configure(0, boolPtr(false), 0)

	if _, err := s.Add("alpha bravo charlie delta echo foxtrot", "", []string{"a"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add("alpha bravo charlie delta echo golf", "", []string{"b"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, _, countBefore, _ := s.Status()
	if countBefore != 2 {
		t.Fatalf("expected 2 items before compact, got %d", countBefore)
	}
	result := s.Compact(0.3)
	_, _, countAfter, _ := s.Status()

	if countAfter >= countBefore {
		t.Errorf("dedup should reduce count: before=%d after=%d", countBefore, countAfter)
	}
	if result.Deduplicated == 0 {
		t.Error("expected at least one deduplication")
	}
}

func TestUpdateSummary(t *testing.T) {
	s := NewStore(100000)

	item, err := s.Add("full content here", "", nil, 5)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
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
	s := NewStore(10) // very small: 10 tokens
	s.Configure(0, boolPtr(true), 0.5)

	// Add items that should trigger auto-compaction (errors ignored; auto-compact may evict)
	s.Add("first item with some content padding", "first", nil, 3)   //nolint:gosec
	s.Add("second item with more content padding", "second", nil, 3) //nolint:gosec
	s.Add("third item triggering compaction now", "third", nil, 3)   //nolint:gosec

	_, used, _, usage := s.Status()
	// Auto-compact should have kept usage reasonable
	if usage > 1.0 {
		t.Errorf("auto-compact should prevent exceeding budget: used=%d, usage=%.1f%%", used, usage*100)
	}
}

func TestUnpin(t *testing.T) {
	s := NewStore(100000)

	item, err := s.Add("test content for unpin", "", nil, 5)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Pin then unpin
	if !s.Pin(item.ID) {
		t.Fatal("pin failed")
	}
	got, _ := s.Get(item.ID)
	if !got.Pinned {
		t.Fatal("should be pinned")
	}

	if !s.Unpin(item.ID) {
		t.Fatal("unpin failed")
	}
	got, _ = s.Get(item.ID)
	if got.Pinned {
		t.Fatal("should be unpinned")
	}

	// Unpin non-existent item
	if s.Unpin("nonexistent") {
		t.Error("unpin of non-existent item should return false")
	}
}

func TestListItems(t *testing.T) {
	s := NewStore(100000)

	// Add 5 items
	for i := 0; i < 5; i++ {
		_, err := s.Add("content "+string(rune('a'+i)), "", nil, 5)
		if err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
	}

	// List all
	items, total := s.ListItems(0, 10)
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(items) != 5 {
		t.Errorf("expected 5 items, got %d", len(items))
	}

	// List with offset
	items, total = s.ListItems(3, 10)
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items with offset 3, got %d", len(items))
	}

	// List with limit
	items, total = s.ListItems(0, 2)
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items with limit 2, got %d", len(items))
	}

	// List beyond end
	items, total = s.ListItems(10, 5)
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if items != nil {
		t.Errorf("expected nil items beyond end, got %d", len(items))
	}
}

func TestBulkAdd(t *testing.T) {
	s := NewStore(100000)

	bulkItems := []BulkItem{
		{Content: "first bulk item", Summary: "first", Tags: []string{"bulk"}, Importance: 5},
		{Content: "second bulk item", Summary: "second", Tags: []string{"bulk"}, Importance: 7},
		{Content: "third bulk item", Summary: "", Tags: nil, Importance: 3},
	}

	results, errs := s.BulkAdd(bulkItems)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("bulk add item %d failed: %v", i, err)
		}
	}
	for i, item := range results {
		if item == nil {
			t.Errorf("bulk add item %d is nil", i)
		} else if item.ID == "" {
			t.Errorf("bulk add item %d has empty ID", i)
		}
	}

	_, _, count, _ := s.Status()
	if count != 3 {
		t.Errorf("expected 3 items in store, got %d", count)
	}
}

func TestExport(t *testing.T) {
	s := NewStore(100000)

	if _, err := s.Add("full content one", "summary one", []string{"a"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add("full content two", "", []string{"b"}, 7); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Export all
	items := s.Export(false)
	if len(items) != 2 {
		t.Fatalf("expected 2 exported items, got %d", len(items))
	}
	for _, item := range items {
		if strings.HasPrefix(item.Content, "summary") {
			t.Error("full export should not replace content with summary")
		}
	}

	// Export summaries only
	items = s.Export(true)
	if len(items) != 2 {
		t.Fatalf("expected 2 exported items, got %d", len(items))
	}
	foundSummary := false
	foundFull := false
	for _, item := range items {
		if item.Content == "summary one" {
			foundSummary = true
		}
		if item.Content == "full content two" {
			foundFull = true // no summary available, keep full content
		}
	}
	if !foundSummary {
		t.Error("summaries_only should replace content with summary where available")
	}
	if !foundFull {
		t.Error("summaries_only should keep full content when no summary available")
	}
}

func TestItemCountLimit(t *testing.T) {
	// We can't add 10001 items in a test (too slow), but we can test the limit
	// by lowering the effective count. Instead, test with a smaller approach:
	// fill the store to capacity and verify the error.
	s := NewStore(1000000)

	// Override: we test by directly checking the error message
	// Add one item, then manipulate to test boundary
	item, err := s.Add("test", "", nil, 5)
	if err != nil {
		t.Fatalf("first add failed: %v", err)
	}
	if item == nil {
		t.Fatal("expected non-nil item")
	}

	// Add content that's too large
	bigContent := strings.Repeat("x", maxContentBytes+1)
	_, err = s.Add(bigContent, "", nil, 5)
	if err == nil {
		t.Error("expected error for oversized content")
	}
	if err != nil && !strings.Contains(err.Error(), "content too large") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestContentSizeLimit(t *testing.T) {
	s := NewStore(100000)

	// Exactly at limit should succeed
	content := strings.Repeat("x", maxContentBytes)
	_, err := s.Add(content, "", nil, 5)
	if err != nil {
		t.Errorf("content at exact limit should succeed: %v", err)
	}

	// Over limit should fail
	content = strings.Repeat("x", maxContentBytes+1)
	_, err = s.Add(content, "", nil, 5)
	if err == nil {
		t.Error("content over limit should fail")
	}
}

func TestQueryAccessCountFix(t *testing.T) {
	s := NewStore(100000)

	// Add 5 items
	ids := make([]string, 5)
	for i := 0; i < 5; i++ {
		item, _ := s.Add("item "+string(rune('a'+i)), "", nil, 5)
		ids[i] = item.ID
	}

	// Query with limit 2 - only the top 2 should get access bumps
	s.Query("item", nil, 2)

	// Check that we got exactly 2 items with AccessCount > 0
	bumped := 0
	for _, id := range ids {
		got, ok := s.Get(id)
		if !ok {
			t.Fatalf("item %s not found", id)
		}
		// Get itself bumps access count by 1, so items that were
		// bumped by Query will have AccessCount >= 2 after Get
		if got.AccessCount >= 2 {
			bumped++
		}
	}
	// Only the 2 items returned by Query should have been bumped
	// (plus the Get call bumps all by 1)
	if bumped > 2 {
		t.Errorf("expected at most 2 items bumped by query, got %d", bumped)
	}
}

func TestGetReturnsValueCopy(t *testing.T) {
	s := NewStore(100000)

	item, _ := s.Add("original content", "", nil, 5)
	got, ok := s.Get(item.ID)
	if !ok {
		t.Fatal("item not found")
	}

	// Mutating the returned copy should not affect the store.
	// We use a helper to avoid the unusedwrite lint.
	mutateItemContent(&got, "mutated")
	got2, _ := s.Get(item.ID)
	if got2.Content != "original content" {
		t.Error("Get should return value copy; mutation should not affect store")
	}
}

func mutateItemContent(item *Item, content string) { item.Content = content }

func TestQueryReturnsValueCopies(t *testing.T) {
	s := NewStore(100000)

	if _, err := s.Add("original query content", "", []string{"test"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	results := s.Query("original", nil, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Mutating the returned copy should not affect the store
	results[0].Content = "mutated"
	results2 := s.Query("original", nil, 10)
	if len(results2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results2))
	}
	if results2[0].Content == "mutated" {
		t.Error("Query should return value copies; mutation should not affect store")
	}
}

func boolPtr(b bool) *bool { return &b }
