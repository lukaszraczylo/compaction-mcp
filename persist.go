package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateVersion = 1

type persistedState struct {
	SavedAt time.Time       `json:"saved_at"`
	Items   []persistedItem `json:"items"`
	Version int             `json:"version"`
	Budget  int             `json:"budget"`
}

type persistedItem struct {
	CreatedAt   time.Time   `json:"created_at"`
	ID          string      `json:"id"`
	Content     string      `json:"content"`
	Summary     string      `json:"summary,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Importance  int         `json:"importance"`
	AccessCount int         `json:"access_count"`
	Tokens      int         `json:"tokens"`
	ContentType ContentType `json:"content_type,omitempty"`
	Pinned      bool        `json:"pinned,omitempty"`
}

// Persister handles file-backed persistence for a Store.
type Persister struct {
	store  *Store
	stopCh chan struct{}
	dir    string
	mu     sync.Mutex
	dirty  bool
}

// snapshot returns a copy of all items and the token budget from the store.
func (s *Store) snapshot() ([]persistedItem, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]persistedItem, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, persistedItem{
			ID:          item.ID,
			Content:     item.Content,
			Summary:     item.Summary,
			Tags:        item.Tags,
			Importance:  item.Importance,
			ContentType: item.ContentType,
			Pinned:      item.Pinned,
			CreatedAt:   item.CreatedAt,
			AccessCount: item.AccessCount,
			Tokens:      item.Tokens,
		})
	}
	return items, s.tokenBudget
}

// restore loads persisted items back into the store.
func (s *Store) restore(items []persistedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, pi := range items {
		item := &Item{
			ID:          pi.ID,
			Content:     pi.Content,
			Summary:     pi.Summary,
			Tags:        pi.Tags,
			Importance:  pi.Importance,
			ContentType: pi.ContentType,
			Pinned:      pi.Pinned,
			CreatedAt:   pi.CreatedAt,
			AccessCount: pi.AccessCount,
			Tokens:      pi.Tokens,
			AccessedAt:  time.Now(),
		}
		s.items[item.ID] = item
		s.usedTokens += item.Tokens
		s.index.Add(item.ID, item.Content, item.Tags)
	}
}

// NewPersister creates a Persister that saves store state to the given directory.
func NewPersister(dir string, store *Store) (*Persister, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Persister{
		dir:   dir,
		store: store,
	}, nil
}

func (p *Persister) stateFile() string {
	return filepath.Join(p.dir, "state.json")
}

func (p *Persister) tmpFile() string {
	return filepath.Join(p.dir, "state.json.tmp")
}

// Save snapshots the store and writes it atomically to state.json.
func (p *Persister) Save() error {
	items, budget := p.store.snapshot()

	state := persistedState{
		Version: stateVersion,
		Budget:  budget,
		Items:   items,
		SavedAt: time.Now(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmp := p.tmpFile()
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, p.stateFile())
}

// Load reads state.json and restores items into the store.
// Returns nil if the file does not exist (fresh start).
// Returns an error if the file exists but cannot be parsed.
func (p *Persister) Load() error {
	data, err := os.ReadFile(p.stateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	p.store.restore(state.Items)
	return nil
}

// Start launches a background goroutine that periodically saves if dirty.
func (p *Persister) Start(interval time.Duration) {
	p.mu.Lock()
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.mu.Lock()
				shouldSave := p.dirty
				p.dirty = false
				p.mu.Unlock()

				if shouldSave {
					_ = p.Save()
				}
			case <-p.stopCh:
				return
			}
		}
	}()
}

// MarkDirty flags the store state as needing a save.
func (p *Persister) MarkDirty() {
	p.mu.Lock()
	p.dirty = true
	p.mu.Unlock()
}

// Stop signals the background goroutine to exit and performs a final save if dirty.
func (p *Persister) Stop() {
	p.mu.Lock()
	if p.stopCh != nil {
		close(p.stopCh)
	}
	shouldSave := p.dirty
	p.dirty = false
	p.mu.Unlock()

	if shouldSave {
		_ = p.Save()
	}
}
