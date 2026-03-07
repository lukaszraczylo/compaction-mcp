package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxItems           = 10000
	maxContentBytes    = 1 << 20 // 1 MiB
	maxDedupCandidates = 500
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
	ContentType ContentType
	Pinned      bool
}

type SummaryCandidate struct {
	ID      string
	Preview string
	Tokens  int
}

type CompactResult struct {
	NeedsSummary []SummaryCandidate
	TokensFreed  int
	TokensBefore int
	TokensAfter  int
	Evicted      int
	Summarized   int
	Deduplicated int
}

type BulkItem struct {
	Content    string
	Summary    string
	Tags       []string
	Importance int
}

type Store struct {
	items                map[string]*Item
	index                *Index
	tokenBudget          int
	usedTokens           int
	autoCompactThreshold float64
	mu                   sync.Mutex
	autoCompact          bool
}

func NewStore(tokenBudget int) *Store {
	return &Store{
		items:                make(map[string]*Item),
		index:                NewIndex(),
		tokenBudget:          tokenBudget,
		autoCompact:          true,
		autoCompactThreshold: 0.9,
	}
}

func (s *Store) Add(content, summary string, tags []string, importance int) (*Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.items) >= maxItems {
		return nil, errors.New("item limit reached (max 10000)")
	}
	if len(content) > maxContentBytes {
		return nil, fmt.Errorf("content too large (%d bytes, max %d)", len(content), maxContentBytes)
	}

	if importance < 1 {
		importance = 5
	} else if importance > 10 {
		importance = 10
	}

	ct := DetectContentType(content)
	if len(tags) == 0 {
		tags = AutoTags(content)
	}

	tokens := EstimateTokens(content)
	item := &Item{
		ID:          newID(),
		Content:     content,
		Summary:     summary,
		Tags:        tags,
		Importance:  importance,
		ContentType: ct,
		CreatedAt:   time.Now(),
		AccessedAt:  time.Now(),
		Tokens:      tokens,
	}

	s.items[item.ID] = item
	s.usedTokens += tokens
	s.index.Add(item.ID, content, tags)

	if s.autoCompact && s.tokenBudget > 0 {
		if float64(s.usedTokens)/float64(s.tokenBudget) > s.autoCompactThreshold {
			s.compactLocked(0.8, false)
		}
	}

	return item, nil
}

func (s *Store) Get(id string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if ok {
		item.AccessedAt = time.Now()
		item.AccessCount++
		return *item, true
	}
	return Item{}, false
}

func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return false
	}
	s.usedTokens -= item.Tokens
	s.index.Remove(id)
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

func (s *Store) Unpin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if ok {
		item.Pinned = false
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

func (s *Store) Query(query string, tags []string, limit int) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 10
	}

	type scored struct {
		item  *Item
		score float64
	}
	var results []scored

	if query != "" {
		// BM25 search ranks results by relevance
		searchResults := s.index.Search(query, 0)
		for _, sr := range searchResults {
			item, ok := s.items[sr.ID]
			if !ok {
				continue
			}
			if len(tags) > 0 && !hasAnyTag(item, tags) {
				continue
			}
			// Combine BM25 relevance with item score
			results = append(results, scored{item: item, score: sr.Score + s.scoreLocked(item)})
		}
	} else {
		// No query text — filter by tags, sort by score
		for _, item := range s.items {
			if len(tags) > 0 && !hasAnyTag(item, tags) {
				continue
			}
			results = append(results, scored{item: item, score: s.scoreLocked(item)})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	// Only bump AccessedAt/AccessCount on items that made the cut
	out := make([]Item, len(results))
	for i, r := range results {
		r.item.AccessedAt = time.Now()
		r.item.AccessCount++
		out[i] = *r.item
	}
	return out
}

func (s *Store) ListItems(offset, limit int) ([]Item, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := len(s.items)

	// Collect and sort by creation time descending
	all := make([]*Item, 0, total)
	for _, item := range s.items {
		all = append(all, item)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}

	if offset >= len(all) {
		return nil, total
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}

	out := make([]Item, end-offset)
	for i, item := range all[offset:end] {
		out[i] = *item
	}
	return out, total
}

func (s *Store) BulkAdd(items []BulkItem) ([]*Item, []error) {
	results := make([]*Item, len(items))
	errs := make([]error, len(items))
	for i, bi := range items {
		item, err := s.Add(bi.Content, bi.Summary, bi.Tags, bi.Importance)
		results[i] = item
		errs[i] = err
	}
	return results, errs
}

func (s *Store) Export(summariesOnly bool) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Item, 0, len(s.items))
	for _, item := range s.items {
		cp := *item
		if summariesOnly && cp.Summary != "" {
			cp.Content = cp.Summary
			cp.Tokens = EstimateTokens(cp.Content)
		}
		out = append(out, cp)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Recall returns status info and the top items by retention score.
// Designed as a single "session start" call to restore working context.
func (s *Store) Recall(limit int) (budget, used, count int, usage float64, items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()

	budget = s.tokenBudget
	used = s.usedTokens
	count = len(s.items)
	if budget > 0 {
		usage = float64(used) / float64(budget)
	}

	if count == 0 || limit <= 0 {
		return
	}

	type scored struct {
		item  *Item
		score float64
	}
	all := make([]scored, 0, count)
	for _, item := range s.items {
		all = append(all, scored{item: item, score: s.scoreLocked(item)})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})

	n := limit
	if n > len(all) {
		n = len(all)
	}
	items = make([]Item, n)
	for i := 0; i < n; i++ {
		items[i] = *all[i].item
	}
	return
}

func (s *Store) scoreLocked(item *Item) float64 {
	if item.Pinned {
		return math.MaxFloat64
	}

	halfLife := DecayHalfLifeMinutes(item.ContentType)
	age := time.Since(item.AccessedAt).Minutes()
	recency := math.Exp(-age / halfLife)

	importance := float64(item.Importance) / 10.0
	importance *= ScoreMultiplier(item.ContentType)
	access := math.Min(math.Log1p(float64(item.AccessCount))/5.0, 1.0)

	var sizePenalty float64
	if s.tokenBudget > 0 {
		sizePenalty = float64(item.Tokens) / float64(s.tokenBudget)
	}

	return (0.4 * importance) + (0.3 * recency) + (0.2 * access) - (0.1 * sizePenalty)
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
	return s.compactLocked(targetUsage, true)
}

func (s *Store) compactLocked(targetUsage float64, fullCompaction bool) CompactResult {
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
	// Sort candidates by score ascending so we promote lowest-scoring items first
	type promoCandidate struct {
		item  *Item
		score float64
	}
	var promoCandidates []promoCandidate
	for _, item := range s.items {
		if item.Pinned || item.Summary == "" || item.Content == item.Summary {
			continue
		}
		promoCandidates = append(promoCandidates, promoCandidate{item: item, score: s.scoreLocked(item)})
	}
	sort.Slice(promoCandidates, func(i, j int) bool {
		return promoCandidates[i].score < promoCandidates[j].score
	})

	for _, pc := range promoCandidates {
		if s.usedTokens <= targetTokens {
			break
		}
		item := pc.item
		oldTokens := item.Tokens
		item.Content = item.Summary
		item.Tokens = EstimateTokens(item.Content)
		saved := oldTokens - item.Tokens
		if saved > 0 {
			s.usedTokens -= saved
			result.Summarized++
			result.TokensFreed += saved
			s.index.Add(item.ID, item.Content, item.Tags)
		}
	}

	// Phase 2: Deduplication — merge items with >70% word overlap (full compaction only)
	if fullCompaction {
		ids := make([]string, 0, len(s.items))
		for id := range s.items {
			ids = append(ids, id)
		}

		// Cap dedup candidates
		if len(ids) > maxDedupCandidates {
			// Sort by score ascending to prioritize merging low-value items
			sort.Slice(ids, func(i, j int) bool {
				return s.scoreLocked(s.items[ids[i]]) < s.scoreLocked(s.items[ids[j]])
			})
			ids = ids[:maxDedupCandidates]
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
						s.index.Remove(ids[j])
						delete(s.items, ids[j])
						merged[ids[j]] = true
					} else {
						b.Tags = mergeTags(b.Tags, a.Tags)
						if b.Summary == "" && a.Summary != "" {
							b.Summary = a.Summary
						}
						s.usedTokens -= a.Tokens
						result.TokensFreed += a.Tokens
						s.index.Remove(ids[i])
						delete(s.items, ids[i])
						merged[ids[i]] = true
						result.Deduplicated++
						break
					}
					result.Deduplicated++
				}
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
			s.index.Remove(item.ID)
			delete(s.items, item.ID)
			result.Evicted++
		}
	}

	// Collect items that could benefit from LLM summarization
	for _, item := range s.items {
		if item.Summary == "" && item.Tokens > 100 {
			preview := item.Content
			if len(preview) > 80 {
				preview = preview[:80]
			}
			result.NeedsSummary = append(result.NeedsSummary, SummaryCandidate{
				ID:      item.ID,
				Tokens:  item.Tokens,
				Preview: preview,
			})
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
