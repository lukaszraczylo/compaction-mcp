package main

import (
	"crypto/rand"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type Item struct {
	CreatedAt   time.Time
	AccessedAt  time.Time
	Content     string
	Summary     string
	ID          string
	Tags        []string
	Tokens      int
	AccessCount int
	Importance  int
	Pinned      bool
}

type CompactResult struct {
	NeedsSummary []*Item
	TokensFreed  int
	TokensBefore int
	TokensAfter  int
	Evicted      int
	Summarized   int
	Deduplicated int
}

type Store struct {
	items                map[string]*Item
	tokenBudget          int
	usedTokens           int
	autoCompactThreshold float64
	mu                   sync.Mutex
	autoCompact          bool
}

func NewStore(tokenBudget int) *Store {
	return &Store{
		items:                make(map[string]*Item),
		tokenBudget:          tokenBudget,
		autoCompact:          true,
		autoCompactThreshold: 0.9,
	}
}

func (s *Store) Add(content, summary string, tags []string, importance int) *Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	if importance < 1 {
		importance = 5
	} else if importance > 10 {
		importance = 10
	}

	tokens := EstimateTokens(content)
	item := &Item{
		ID:         newID(),
		Content:    content,
		Summary:    summary,
		Tags:       tags,
		Importance: importance,
		CreatedAt:  time.Now(),
		AccessedAt: time.Now(),
		Tokens:     tokens,
	}

	s.items[item.ID] = item
	s.usedTokens += tokens

	if s.autoCompact && s.tokenBudget > 0 {
		if float64(s.usedTokens)/float64(s.tokenBudget) > s.autoCompactThreshold {
			s.compactLocked(0.8)
		}
	}

	return item
}

func (s *Store) Get(id string) (*Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if ok {
		item.AccessedAt = time.Now()
		item.AccessCount++
	}
	return item, ok
}

func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return false
	}
	s.usedTokens -= item.Tokens
	delete(s.items, id)
	return true
}

func (s *Store) Pin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if ok {
		item.Pinned = true
	}
	return ok
}

func (s *Store) UpdateSummary(id, summary string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if ok {
		item.Summary = summary
	}
	return ok
}

func (s *Store) Query(query string, tags []string, limit int) []*Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 10
	}

	queryWords := wordSet(query)
	var results []*Item

	for _, item := range s.items {
		if len(tags) > 0 && !hasAnyTag(item, tags) {
			continue
		}
		item.AccessedAt = time.Now()
		item.AccessCount++
		results = append(results, item)
	}

	sort.Slice(results, func(i, j int) bool {
		return s.queryScore(results[i], queryWords) > s.queryScore(results[j], queryWords)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (s *Store) scoreLocked(item *Item) float64 {
	if item.Pinned {
		return math.MaxFloat64
	}

	age := time.Since(item.AccessedAt).Minutes()
	recency := math.Exp(-age / 120.0) // half-life ~2 hours

	importance := float64(item.Importance) / 10.0
	access := math.Log1p(float64(item.AccessCount)) / 5.0

	var sizePenalty float64
	if s.tokenBudget > 0 {
		sizePenalty = float64(item.Tokens) / float64(s.tokenBudget)
	}

	return (0.4 * importance) + (0.3 * recency) + (0.2 * access) - (0.1 * sizePenalty)
}

func (s *Store) queryScore(item *Item, queryWords map[string]struct{}) float64 {
	base := s.scoreLocked(item)
	if len(queryWords) == 0 {
		return base
	}

	contentWords := wordSet(item.Content)
	if item.Summary != "" {
		for w := range wordSet(item.Summary) {
			contentWords[w] = struct{}{}
		}
	}
	for _, tag := range item.Tags {
		contentWords[strings.ToLower(tag)] = struct{}{}
	}

	return base + (0.5 * jaccardSimilarity(queryWords, contentWords))
}

func (s *Store) Status() (budget, used, count int, usage float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	budget = s.tokenBudget
	used = s.usedTokens
	count = len(s.items)
	if budget > 0 {
		usage = float64(used) / float64(budget)
	}
	return
}

func (s *Store) BudgetTight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tokenBudget <= 0 {
		return false
	}
	return float64(s.usedTokens)/float64(s.tokenBudget) > 0.8
}

func (s *Store) Configure(budget int, autoCompact *bool, threshold float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if budget > 0 {
		s.tokenBudget = budget
	}
	if autoCompact != nil {
		s.autoCompact = *autoCompact
	}
	if threshold > 0 && threshold <= 1.0 {
		s.autoCompactThreshold = threshold
	}
}

func (s *Store) AutoCompact() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCompact
}

func (s *Store) AutoCompactThreshold() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCompactThreshold
}

func (s *Store) Compact(targetUsage float64) CompactResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactLocked(targetUsage)
}

func (s *Store) compactLocked(targetUsage float64) CompactResult {
	result := CompactResult{TokensBefore: s.usedTokens}

	if s.tokenBudget <= 0 {
		result.TokensAfter = s.usedTokens
		return result
	}

	targetTokens := int(float64(s.tokenBudget) * targetUsage)
	if s.usedTokens <= targetTokens {
		result.TokensAfter = s.usedTokens
		return result
	}

	// Phase 1: Summary promotion — replace content with summary to save tokens
	for _, item := range s.items {
		if s.usedTokens <= targetTokens {
			break
		}
		if item.Pinned || item.Summary == "" || item.Content == item.Summary {
			continue
		}
		oldTokens := item.Tokens
		item.Content = item.Summary
		item.Tokens = EstimateTokens(item.Content)
		saved := oldTokens - item.Tokens
		if saved > 0 {
			s.usedTokens -= saved
			result.Summarized++
			result.TokensFreed += saved
		}
	}

	// Phase 2: Deduplication — merge items with >70% word overlap
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	merged := make(map[string]bool)

	for i := 0; i < len(ids); i++ {
		if merged[ids[i]] {
			continue
		}
		a := s.items[ids[i]]
		if a == nil || a.Pinned {
			continue
		}
		aWords := wordSet(a.Content)

		for j := i + 1; j < len(ids); j++ {
			if merged[ids[j]] {
				continue
			}
			b := s.items[ids[j]]
			if b == nil {
				continue
			}

			if jaccardSimilarity(aWords, wordSet(b.Content)) > 0.7 {
				// Keep higher-scoring item, merge tags
				if s.scoreLocked(a) >= s.scoreLocked(b) {
					a.Tags = mergeTags(a.Tags, b.Tags)
					if a.Summary == "" && b.Summary != "" {
						a.Summary = b.Summary
					}
					s.usedTokens -= b.Tokens
					result.TokensFreed += b.Tokens
					delete(s.items, ids[j])
					merged[ids[j]] = true
				} else {
					b.Tags = mergeTags(b.Tags, a.Tags)
					if b.Summary == "" && a.Summary != "" {
						b.Summary = a.Summary
					}
					s.usedTokens -= a.Tokens
					result.TokensFreed += a.Tokens
					delete(s.items, ids[i])
					merged[ids[i]] = true
					break
				}
				result.Deduplicated++
			}
		}
	}

	// Phase 3: Evict lowest-scoring non-pinned items
	if s.usedTokens > targetTokens {
		evictable := make([]*Item, 0)
		for _, item := range s.items {
			if !item.Pinned {
				evictable = append(evictable, item)
			}
		}
		sort.Slice(evictable, func(i, j int) bool {
			return s.scoreLocked(evictable[i]) < s.scoreLocked(evictable[j])
		})

		for _, item := range evictable {
			if s.usedTokens <= targetTokens {
				break
			}
			s.usedTokens -= item.Tokens
			result.TokensFreed += item.Tokens
			delete(s.items, item.ID)
			result.Evicted++
		}
	}

	// Collect items that could benefit from LLM summarization
	for _, item := range s.items {
		if item.Summary == "" && item.Tokens > 100 {
			result.NeedsSummary = append(result.NeedsSummary, item)
		}
	}

	result.TokensAfter = s.usedTokens
	return result
}

// --- helpers ---

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return fmt.Sprintf("%x", b)
}

func wordSet(s string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for w := range a {
		if _, ok := b[w]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func hasAnyTag(item *Item, tags []string) bool {
	tagSet := make(map[string]struct{}, len(item.Tags))
	for _, t := range item.Tags {
		tagSet[strings.ToLower(t)] = struct{}{}
	}
	for _, t := range tags {
		if _, ok := tagSet[strings.ToLower(t)]; ok {
			return true
		}
	}
	return false
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, t := range a {
		lower := strings.ToLower(t)
		if _, ok := seen[lower]; !ok {
			seen[lower] = struct{}{}
			result = append(result, t)
		}
	}
	for _, t := range b {
		lower := strings.ToLower(t)
		if _, ok := seen[lower]; !ok {
			seen[lower] = struct{}{}
			result = append(result, t)
		}
	}
	return result
}
