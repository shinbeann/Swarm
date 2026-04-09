package main

import (
    "fmt"
    "log"
    "sync"
    "time"
)

const (
    VerificationQuorum = 3 // minimum reporters to verify a landmark
)

// KnowledgeStore stores discovered landmarks.
type KnowledgeStore struct {
	mu         sync.RWMutex
	entries    map[LandmarkID]*LandmarkEntry
	verifiedCh chan<- *LandmarkEntry
}

func NewKnowledgeStore() *KnowledgeStore {
	return &KnowledgeStore{entries: make(map[LandmarkID]*LandmarkEntry)}
}

// SetVerifiedCh wires up the one-way notification channel.
// Call this once during Robot construction, before gossip starts.
func (ks *KnowledgeStore) SetVerifiedCh(ch chan<- *LandmarkEntry) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.verifiedCh = ch
}

func (ks *KnowledgeStore) Add(id LandmarkID, ltype LandmarkType, loc Location, reporter RobotID, timestamp int) bool {
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
            Verified:  false,
        }
        ks.entries[id] = entry

        // LOG 1: new entry created
        if ltype == LandmarkCasualty {
            log.Printf("[KS] NEW casualty %s at (%.1f,%.1f) first reporter=%s ts=%d",
                id, loc.X, loc.Y, reporter, timestamp)
        }
    }

    if _, alreadyReported := entry.Reporters[reporter]; !alreadyReported {
        entry.Reporters[reporter] = timestamp

        // LOG 2: reporter added — show running count toward quorum
        if entry.Type == LandmarkCasualty {
            log.Printf("[KS] casualty %s reporter added: %s | total reporters=%d verified=%v",
                id, reporter, len(entry.Reporters), entry.Verified)
        }
    }

    if !entry.Verified && len(entry.Reporters) >= VerificationQuorum {
        entry.Verified = true
        log.Printf("[KS] casualty %s VERIFIED — %d reporters reached quorum", id, len(entry.Reporters))
        if ks.verifiedCh != nil {
            select {
            case ks.verifiedCh <- entry:
            default:
            }
        }
        return true
    }
    return false
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

    if e, exists := ks.entries[id]; exists {
        log.Printf("[KS] DELETE %s (%s) — was verified=%v reporters=%d",
            id, e.Type, e.Verified, len(e.Reporters))
    }
    delete(ks.entries, id)
}

// UnverifiedCasualties returns casualty entries not yet reported by reporterID.
// The movement loop calls this to find casualties worth moving toward.
func (ks *KnowledgeStore) UnverifiedCasualties(self RobotID) []*LandmarkEntry {
    ks.mu.RLock()
    defer ks.mu.RUnlock()

    var result []*LandmarkEntry
    for _, e := range ks.entries {
        if e.Type != LandmarkCasualty {
            continue
        }
        if e.Verified {
            continue
        }
        if _, alreadyReported := e.Reporters[self]; alreadyReported {
            continue
        }
        result = append(result, e)
    }

    // LOG 3: what the movement loop is working with this tick
    if len(result) > 0 {
        ids := make([]string, len(result))
        for i, e := range result {
            ids[i] = fmt.Sprintf("%s(%d reporters)", e.ID, len(e.Reporters))
        }
        log.Printf("[KS] %s unverified casualties needing visit: %v", self, ids)
    }
    return result
}
