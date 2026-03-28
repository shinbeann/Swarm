package main

import (
	"bytes"
	"testing"
)

// Fo1: Follower lag prevLogIndex mismatch rejection
func TestRaftFollowerLagRejection(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	// Leader thinks follower is up to date (nextIndex = 3)
	// But follower is actually empty.
	r1.nextIndex[r2.ID] = 3
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("y"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("z"))

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	// prevLogIndex will be 2 (term 1)

	resp := r2.HandleAppendEntries(req)
	if resp.GetSuccess() {
		t.Fatalf("expected follower to reject due to lag")
	}
}

// Fo2: Follower missing logs catch-up and commitIndex update
func TestRaftFollowerCatchupAndCommitUpdate(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	replicateLog(r1, r2)
	replicateLog(r1, r3)
	
	r1.mu.Lock()
	r1.advanceCommitIndexLocked() // commitIndex = 0
	r1.mu.Unlock()

	// Leader writes another log (index 1) and commits it
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("y"))
	replicateLog(r1, r3) // Replicate only to r3 to get majority (r1, r3)
	
	r1.mu.Lock()
	r1.advanceCommitIndexLocked() // commitIndex = 1
	r1.mu.Unlock()

	// Now r2 is lagging (only has index 0). Leader's commit is 1.
	// Because r2 hasn't received a heartbeat since index 0 was committed,
	// its commitIndex is still -1.
	if r2.commitIndex != -1 {
		t.Fatalf("expected r2 commitIndex -1, got %d", r2.commitIndex)
	}

	// 1. Leader sends heartbeat/catchup to r2
	replicateLog(r1, r2)

	// 2. r2 should have received index 1 and its commitIndex should advance to 1
	r2.mu.Lock()
	defer r2.mu.Unlock()

	if len(r2.raftLog) != 2 {
		t.Fatalf("expected r2 to catch up 2 logs, got %d", len(r2.raftLog))
	}
	if r2.commitIndex != 1 {
		t.Fatalf("expected r2 commitIndex %d, got %d", 1, r2.commitIndex)
	}
}

// I1: Heartbeat idempotency
func TestRaftFollowerHeartbeatIdempotency(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	replicateLog(r1, r2)
	
	r2.mu.Lock()
	r2LogLen := len(r2.raftLog)
	r2.mu.Unlock()

	// Send consecutive empty heartbeats
	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	// Clear entries to simulate pure heartbeat
	req.Entries = nil
	req.PrevLogIndex = int64(r2LogLen - 1)
	req.PrevLogTerm = r1.raftTerm

	for i := 0; i < 5; i++ {
		resp := r2.HandleAppendEntries(req)
		if !resp.GetSuccess() {
			t.Fatalf("expected heartbeat to succeed")
		}
	}

	r2.mu.Lock()
	if len(r2.raftLog) != r2LogLen {
		t.Fatalf("expected log length unchanged after heartbeats")
	}
	r2.mu.Unlock()
}

// I2: Duplicate AppendEntries idempotency
func TestRaftFollowerDuplicateAppendIdempotency(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	req := r1.buildAppendEntriesRequestForPeer(r2.ID)

	// Send SAME request twice (e.g. dropped response retry)
	resp1 := r2.HandleAppendEntries(req)
	resp2 := r2.HandleAppendEntries(req)

	if !resp1.GetSuccess() || !resp2.GetSuccess() {
		t.Fatalf("expected both append attempts to succeed")
	}

	r2.mu.Lock()
	if len(r2.raftLog) != 1 {
		t.Fatalf("expected log to have exactly 1 entry even after duplicates, got %d", len(r2.raftLog))
	}
	if !bytes.Equal(r2.raftLog[0].Payload, []byte("x")) {
		t.Fatalf("expected log data to match")
	}
	r2.mu.Unlock()
}
