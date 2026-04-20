package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	VerificationQuorum = 3 // minimum distinct reporters required for casualty verification
)

// KnowledgeStore stores discovered landmarks.
type KnowledgeStore struct {
	mu         sync.RWMutex
	entries    map[LandmarkID]*LandmarkEntry
	verifiedCh chan<- *LandmarkEntry // optional: quorum reached → Raft (set via SetVerifiedCh)
}

func NewKnowledgeStore() *KnowledgeStore {
	return &KnowledgeStore{
		entries: make(map[LandmarkID]*LandmarkEntry),
	}
}

func cloneLandmarkEntry(entry *LandmarkEntry) *LandmarkEntry {
	if entry == nil {
		return nil
	}

	clone := *entry
	if entry.Reporters != nil {
		clone.Reporters = make(map[RobotID]int, len(entry.Reporters))
		for reporter, timestamp := range entry.Reporters {
			clone.Reporters[reporter] = timestamp
		}
	}

	return &clone
}

// SetVerifiedCh wires the one-way notification channel for casualty quorum (awaiting Raft commit).
// Call once during Robot construction before gossip runs.
func (ks *KnowledgeStore) SetVerifiedCh(ch chan<- *LandmarkEntry) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.verifiedCh = ch
}

// insert or update a landmark from a single robot ID
func (ks *KnowledgeStore) Add(id LandmarkID, ltype LandmarkType, loc Location, reporter RobotID, timestamp int) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	entry, created := ks.ensureEntryLocked(id, ltype, loc)
	if created && ltype == LandmarkCasualty {
		log.Printf("[KS] NEW casualty %s at (%.1f,%.1f) first reporter=%s ts=%d",
			id, loc.X, loc.Y, reporter, timestamp)
	}

	return ks.mergeReportersLocked(entry, map[RobotID]int{reporter: timestamp}, true, timestamp)
}

// merge full landmark entry
func (ks *KnowledgeStore) MergeSnapshot(snapshot *LandmarkEntry, now int) bool {
	if snapshot == nil {
		return false
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	entry, _ := ks.ensureEntryLocked(snapshot.ID, snapshot.Type, snapshot.Location)
	newlyVerified := ks.mergeReportersLocked(entry, snapshot.Reporters, true, now)
	if snapshot.Committed && !entry.Committed {
		entry.Committed = true
		log.Printf("[KS] casualty %s COMMITTED via merged snapshot", entry.ID)
		entry.Verified = true
	}
	return newlyVerified
}

// ApplyCasualtyCommitted marks a verified casualty as committed once the Raft log entry is applied.
func (ks *KnowledgeStore) ApplyCasualtyCommitted(snapshot *LandmarkEntry) {
	if snapshot == nil {
		return
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	entry, _ := ks.ensureEntryLocked(snapshot.ID, snapshot.Type, snapshot.Location)
	ks.mergeReportersLocked(entry, snapshot.Reporters, false, 0)
	entry.Verified = true
	if !entry.Committed {
		entry.Committed = true
		log.Printf("[KS] casualty %s COMMITTED via committed raft log", entry.ID)
	}
}

// ApplyCasualtyVerified is an alias for ApplyCasualtyCommitted kept for robot.go compatibility.
func (ks *KnowledgeStore) ApplyCasualtyVerified(snapshot *LandmarkEntry) {
	ks.ApplyCasualtyCommitted(snapshot)
}

func (ks *KnowledgeStore) GetAll() []*LandmarkEntry {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	result := make([]*LandmarkEntry, 0, len(ks.entries))
	for _, entry := range ks.entries {
		result = append(result, cloneLandmarkEntry(entry))
	}
	return result
}

// PendingCasualtyVerifications returns casualty entries that have reached quorum
// but have not yet been committed by a Raft entry.
func (ks *KnowledgeStore) PendingCasualtyVerifications() []*LandmarkEntry {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	result := make([]*LandmarkEntry, 0)
	for _, entry := range ks.entries {
		if entry.Type != LandmarkCasualty {
			continue
		}
		if !entry.Verified {
			continue
		}
		if entry.Committed {
			continue
		}
		result = append(result, cloneLandmarkEntry(entry))
	}

	return result
}

func (ks *KnowledgeStore) ensureEntryLocked(id LandmarkID, ltype LandmarkType, loc Location) (*LandmarkEntry, bool) {
	entry, exists := ks.entries[id]
	if exists {
		if entry.Reporters == nil {
			entry.Reporters = make(map[RobotID]int)
		}
		return entry, false
	}

	entry = &LandmarkEntry{
		ID:        id,
		Type:      ltype,
		Location:  loc,
		Reporters: make(map[RobotID]int),
		FirstSeen: time.Now(),
		Verified:  false,
		Committed: false,
	}
	ks.entries[id] = entry
	return entry, true
}

func (ks *KnowledgeStore) mergeReportersLocked(entry *LandmarkEntry, reporters map[RobotID]int, notifyVerification bool, now int) bool {
	if entry.Reporters == nil {
		entry.Reporters = make(map[RobotID]int)
	}

	added := false
	for reporter, timestamp := range reporters {
		if _, alreadyReported := entry.Reporters[reporter]; alreadyReported {
			continue
		}
		entry.Reporters[reporter] = timestamp
		added = true

		if entry.Type == LandmarkCasualty {
			log.Printf("[KS] casualty %s reporter added: %s | total reporters=%d verified=%v",
				entry.ID, reporter, len(entry.Reporters), entry.Verified)
		}
	}

	// Bump entry Lamport timestamp whenever new information arrives.
	// now=0 is passed by Raft apply paths which must not affect the gossip watermark.
	if added && now > entry.Timestamp {
		entry.Timestamp = now
	}

	if entry.Type == LandmarkCasualty && len(entry.Reporters) >= VerificationQuorum && !entry.Verified {
		entry.Verified = true
		if notifyVerification {
			log.Printf("[KS] casualty %s reached verification quorum (%d reporters); awaiting raft commit",
				entry.ID, len(entry.Reporters))
			if ks.verifiedCh != nil {
				select {
				case ks.verifiedCh <- cloneLandmarkEntry(entry):
				default:
				}
			}
		}
		return true
	}

	return false
}

// delete entry and its commit state.
func (ks *KnowledgeStore) Remove(id LandmarkID) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if e, exists := ks.entries[id]; exists {
		log.Printf("[KS] DELETE %s (%s) — was verified=%v committed=%v reporters=%d",
			id, e.Type, e.Verified, e.Committed, len(e.Reporters))
	}
	delete(ks.entries, id)
}

// VerifiedCasualtyIDs returns casualty ids that have reached local verification quorum.
func (ks *KnowledgeStore) VerifiedCasualtyIDs() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	result := make([]string, 0, len(ks.entries))
	for _, entry := range ks.entries {
		if entry.Type != LandmarkCasualty || !entry.Verified {
			continue
		}
		result = append(result, string(entry.ID))
	}

	return result
}


// UnverifiedCasualties returns casualty entries that have not yet reached quorum.
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