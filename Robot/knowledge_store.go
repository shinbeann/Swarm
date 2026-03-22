package main

import (
	"sync"
	"time"
)

// KnowledgeStore stores discovered landmarks.
type KnowledgeStore struct {
	mu      sync.RWMutex
	entries map[LandmarkID]*LandmarkEntry
}

func NewKnowledgeStore() *KnowledgeStore {
	return &KnowledgeStore{entries: make(map[LandmarkID]*LandmarkEntry)}
}

func (ks *KnowledgeStore) Add(id LandmarkID, ltype LandmarkType, loc Location, reporter RobotID, timestamp int) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	entry, exists := ks.entries[id]
	if !exists {
		entry = &LandmarkEntry{
			ID:        id,
			Type:      ltype,
			Location:  loc,
			Reporters: make(map[RobotID]int),
			FirstSeen: time.Now(),
		}
		ks.entries[id] = entry
	}

	entry.Reporters[reporter] = timestamp
}

func (ks *KnowledgeStore) GetAll() []*LandmarkEntry {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	result := make([]*LandmarkEntry, 0, len(ks.entries))
	for _, entry := range ks.entries {
		result = append(result, entry)
	}
	return result
}

func (ks *KnowledgeStore) Remove(id LandmarkID) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	delete(ks.entries, id)
}
