package main

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Index is a BM25 inverted index for full-text search over stored documents.
type Index struct {
	docs     map[string]map[string]int      // docID -> term -> frequency
	docLen   map[string]int                 // docID -> total terms
	postings map[string]map[string]struct{} // term -> set of docIDs
	docTags  map[string]map[string]struct{} // docID -> tag set (boosted 5x)
	n        int
	avgDL    float64
}

// SearchResult holds a document ID and its BM25 relevance score.
type SearchResult struct {
	ID    string
	Score float64
}

// NewIndex creates an empty BM25 index.
func NewIndex() *Index {
	return &Index{
		docs:     make(map[string]map[string]int),
		docLen:   make(map[string]int),
		postings: make(map[string]map[string]struct{}),
		docTags:  make(map[string]map[string]struct{}),
	}
}

// Add indexes a document with the given content and tags.
// Tags are stored separately and receive a 5x score boost during search.
func (idx *Index) Add(id, content string, tags []string) {
	// Remove first if already present to avoid stale data.
	if _, exists := idx.docs[id]; exists {
		idx.Remove(id)
	}

	tokens := tokenize(content)
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}

	idx.docs[id] = tf
	idx.docLen[id] = len(tokens)

	for term := range tf {
		if idx.postings[term] == nil {
			idx.postings[term] = make(map[string]struct{})
		}
		idx.postings[term][id] = struct{}{}
	}

	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		for _, t := range tokenize(tag) {
			tagSet[t] = struct{}{}
		}
	}
	idx.docTags[id] = tagSet

	idx.n++
	idx.recalcAvgDL()
}

// Remove deletes a document from the index.
func (idx *Index) Remove(id string) {
	tf, ok := idx.docs[id]
	if !ok {
		return
	}

	for term := range tf {
		if set, exists := idx.postings[term]; exists {
			delete(set, id)
			if len(set) == 0 {
				delete(idx.postings, term)
			}
		}
	}

	delete(idx.docs, id)
	delete(idx.docLen, id)
	delete(idx.docTags, id)

	idx.n--
	idx.recalcAvgDL()
}

// Search returns the top `limit` documents ranked by BM25 score for the query.
// Tag matches receive a 5x boost on top of the BM25 score.
func (idx *Index) Search(query string, limit int) []SearchResult {
	terms := tokenize(query)
	if len(terms) == 0 || idx.n == 0 {
		return nil
	}

	const (
		k1       = 1.2
		b        = 0.75
		tagBoost = 5.0
	)

	scores := make(map[string]float64)

	for _, term := range terms {
		docSet, ok := idx.postings[term]
		if !ok {
			continue
		}
		df := float64(len(docSet))
		idf := math.Log((float64(idx.n)-df+0.5)/(df+0.5) + 1.0)

		for docID := range docSet {
			tfVal := float64(idx.docs[docID][term])
			dl := float64(idx.docLen[docID])
			num := tfVal * (k1 + 1)
			denom := tfVal + k1*(1-b+b*(dl/idx.avgDL))
			scores[docID] += idf * (num / denom)
		}

		// Tag boost: add 5x the IDF-weighted score for docs whose tags match.
		for docID, tagSet := range idx.docTags {
			if _, hit := tagSet[term]; hit {
				dl := float64(idx.docLen[docID])
				// Use a synthetic TF of 1 for tag matches.
				num := 1.0 * (k1 + 1)
				denom := 1.0 + k1*(1-b+b*(dl/idx.avgDL))
				scores[docID] += tagBoost * idf * (num / denom)
			}
		}
	}

	results := make([]SearchResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, SearchResult{ID: id, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (idx *Index) recalcAvgDL() {
	if idx.n == 0 {
		idx.avgDL = 0
		return
	}
	total := 0
	for _, dl := range idx.docLen {
		total += dl
	}
	idx.avgDL = float64(total) / float64(idx.n)
}

// camelRe matches boundaries in camelCase identifiers (e.g. "handleCompact").
var camelRe = regexp.MustCompile(`([a-z])([A-Z])`)

// tokenize splits text into lowercase terms, handling camelCase and snake_case.
// Tokens shorter than 2 characters are filtered out.
func tokenize(s string) []string {
	// Split camelCase: insert space at lowercase-to-uppercase boundary.
	s = camelRe.ReplaceAllString(s, "${1} ${2}")

	// Split on any non-letter, non-digit character (handles snake_case, punctuation, whitespace).
	splitter := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}
	parts := strings.FieldsFunc(strings.ToLower(s), splitter)

	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 2 {
			tokens = append(tokens, p)
		}
	}
	return tokens
}
