package main

import (
	"bytes"
	"testing"
)

// Objective: verify a lagging follower rejects an AppendEntries request with a mismatched prevLogIndex.
// Expected output: the append response is unsuccessful.
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

// Objective: verify a leader can fan out the same append request to every follower and each follower acknowledges it.
// Expected output: every follower accepts the append and stores the new entry.
func TestRaftLeaderAppendFanOutToAllFollowers(t *testing.T) {
	robots := setupCluster(4)
	r1, r2, r3, r4 := robots[0], robots[1], robots[2], robots[3]

	electLeader(t, r1, r2, r3, r4)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("fanout"))

	followers := []*Robot{r2, r3, r4}
	for _, follower := range followers {
		if !replicateLog(r1, follower) {
			t.Fatalf("expected append to succeed for %s", follower.ID)
		}
	}

	r1.mu.Lock()
	defer r1.mu.Unlock()
	for _, follower := range followers {
		if r1.nextIndex[follower.ID] != 1 {
			t.Fatalf("expected nextIndex for %s to advance to 1, got %d", follower.ID, r1.nextIndex[follower.ID])
		}
		if r1.matchIndex[follower.ID] != 0 {
			t.Fatalf("expected matchIndex for %s to be 0, got %d", follower.ID, r1.matchIndex[follower.ID])
		}
	}
	if r1.commitIndex != 0 {
		t.Fatalf("expected leader commitIndex to advance to 0 after quorum, got %d", r1.commitIndex)
	}
}

// Objective: verify a lagging follower catches up replicated logs and advances commitIndex on heartbeat.
// Expected output: r2 receives both entries and advances commitIndex to 1.
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

// Objective: verify repeated empty heartbeats do not duplicate log entries.
// Expected output: every heartbeat succeeds and the follower log length stays unchanged.
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

// Objective: verify duplicate AppendEntries retries are idempotent.
// Expected output: both appends succeed but the follower still stores exactly one log entry.
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
