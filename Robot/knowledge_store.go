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
    quorumSeen map[LandmarkID]bool
}

func NewKnowledgeStore() *KnowledgeStore {
    return &KnowledgeStore{
        entries:    make(map[LandmarkID]*LandmarkEntry),
        quorumSeen: make(map[LandmarkID]bool),
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

    entry, created := ks.ensureEntryLocked(id, ltype, loc)
    if created && ltype == LandmarkCasualty {
        log.Printf("[KS] NEW casualty %s at (%.1f,%.1f) first reporter=%s ts=%d",
            id, loc.X, loc.Y, reporter, timestamp)
    }

    return ks.mergeReportersLocked(entry, map[RobotID]int{reporter: timestamp}, true)
}

func (ks *KnowledgeStore) MergeSnapshot(snapshot *LandmarkEntry) bool {
    if snapshot == nil {
        return false
    }

    ks.mu.Lock()
    defer ks.mu.Unlock()

    entry, _ := ks.ensureEntryLocked(snapshot.ID, snapshot.Type, snapshot.Location)
    return ks.mergeReportersLocked(entry, snapshot.Reporters, true)
}

// ApplyCasualtyVerified marks a casualty as verified only after a committed Raft log entry is applied.
func (ks *KnowledgeStore) ApplyCasualtyVerified(snapshot *LandmarkEntry) {
    if snapshot == nil {
        return
    }

    ks.mu.Lock()
    defer ks.mu.Unlock()

    entry, _ := ks.ensureEntryLocked(snapshot.ID, snapshot.Type, snapshot.Location)
    ks.mergeReportersLocked(entry, snapshot.Reporters, false)
    if !entry.Verified {
        entry.Verified = true
        log.Printf("[KS] casualty %s VERIFIED via committed raft log", entry.ID)
    }
    ks.quorumSeen[entry.ID] = true
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
    }
    ks.entries[id] = entry
    return entry, true
}

func (ks *KnowledgeStore) mergeReportersLocked(entry *LandmarkEntry, reporters map[RobotID]int, notifyQuorum bool) bool {
    if entry.Reporters == nil {
        entry.Reporters = make(map[RobotID]int)
    }

    for reporter, timestamp := range reporters {
        if _, alreadyReported := entry.Reporters[reporter]; alreadyReported {
            continue
        }
        entry.Reporters[reporter] = timestamp

        if entry.Type == LandmarkCasualty {
            log.Printf("[KS] casualty %s reporter added: %s | total reporters=%d verified=%v",
                entry.ID, reporter, len(entry.Reporters), entry.Verified)
        }
    }

    if entry.Type == LandmarkCasualty && !ks.quorumSeen[entry.ID] && len(entry.Reporters) >= VerificationQuorum {
        ks.quorumSeen[entry.ID] = true
        log.Printf("[KS] casualty %s reached quorum (%d reporters); awaiting raft commit",
            entry.ID, len(entry.Reporters))
        if notifyQuorum && ks.verifiedCh != nil {
            select {
            case ks.verifiedCh <- cloneLandmarkEntry(entry):
            default:
            }
        }
        return true
    }

    return false
}

func (ks *KnowledgeStore) Remove(id LandmarkID) {
    ks.mu.Lock()
    defer ks.mu.Unlock()

    if e, exists := ks.entries[id]; exists {
        log.Printf("[KS] DELETE %s (%s) — was verified=%v reporters=%d",
            id, e.Type, e.Verified, len(e.Reporters))
    }
    delete(ks.entries, id)
    delete(ks.quorumSeen, id)
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
