package main

import (
	"bytes"
	"testing"
)

// Objective: verify conflicting follower logs are truncated and replaced by the leader's entries.
// Expected output: the follower rejects the conflict first, then accepts the leader log and ends with the leader's two-entry history.
func TestRaftLogConsistencyTruncation(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	// r2 has a divergent log:
	// Index 0: Term 1, cmd "a"
	// Index 1: Term 1, cmd "b"
	// Index 2: Term 1, cmd "c"
	manuallyAppendLog(r2, 1, "cmd", []byte("a"))
	manuallyAppendLog(r2, 1, "cmd", []byte("b"))
	manuallyAppendLog(r2, 1, "cmd", []byte("c"))

	// r1 is leader of Term 2. Its log:
	// Index 0: Term 1, cmd "a" (agrees)
	// Index 1: Term 2, cmd "x" (conflicts at index 1)
	r1.raftState = raftLeader
	r1.raftTerm = 2
	manuallyAppendLog(r1, 1, "cmd", []byte("a"))
	manuallyAppendLog(r1, 2, "cmd", []byte("x"))

	// r1 thinks r2 nextIndex is 2 (so prevLogIndex is 1, term 2)
	r1.nextIndex[r2.ID] = 2

	req1 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp1 := r2.HandleAppendEntries(req1)
	r1.handleAppendResponse(r2.ID, req1, resp1)

	// r2 rejects because at index 1, its term is 1, but leader prevLogTerm is 2
	if resp1.GetSuccess() {
		t.Fatalf("expected r2 to reject due to conflict at prevLogIndex 1")
	}

	// r1 backtracks nextIndex to 1, sends prevLogIndex 0, term 1
	r1.mu.Lock()
	if r1.nextIndex[r2.ID] != 1 {
		t.Fatalf("expected leader to backtrack nextIndex to 1")
	}
	r1.mu.Unlock()

	req2 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	// req2 entries contains Index 1 (cmd "x")

	resp2 := r2.HandleAppendEntries(req2)
	r1.handleAppendResponse(r2.ID, req2, resp2)

	// r2 should accept, truncate index 1 and 2, and append leader's index 1
	if !resp2.GetSuccess() {
		t.Fatalf("expected r2 to accept after backtracking")
	}

	r2.mu.Lock()
	defer r2.mu.Unlock()

	if len(r2.raftLog) != 2 {
		t.Fatalf("expected r2 log to be truncated and replace with leader's, length %d", len(r2.raftLog))
	}
	if !bytes.Equal(r2.raftLog[1].Payload, []byte("x")) {
		t.Fatalf("expected r2 to have accepted 'x' at index 1")
	}
}

// Objective: verify a follower rejects an append when the leader's log is behind the follower's conflicting history.
// Expected output: the append response is unsuccessful and the leader backtracks nextIndex.
func TestRaftLogConsistencyRejectsStaleLeaderAppend(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	r1.raftState = raftLeader
	r1.raftTerm = 2
	r2.raftTerm = 2

	manuallyAppendLog(r1, 1, "cmd", []byte("a"))
	manuallyAppendLog(r1, 1, "cmd", []byte("b"))
	manuallyAppendLog(r2, 1, "cmd", []byte("a"))
	manuallyAppendLog(r2, 2, "cmd", []byte("conflict"))

	r1.nextIndex[r2.ID] = 2

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp := r2.HandleAppendEntries(req)
	r1.handleAppendResponse(r2.ID, req, resp)

	if resp.GetSuccess() {
		t.Fatalf("expected r2 to reject stale leader append")
	}

	r1.mu.Lock()
	defer r1.mu.Unlock()
	if r1.nextIndex[r2.ID] != 1 {
		t.Fatalf("expected leader to backtrack nextIndex to 1, got %d", r1.nextIndex[r2.ID])
	}
}

// Objective: verify leaders cannot directly commit old-term entries and only commit them indirectly through a current-term entry.
// Expected output: the old entry remains uncommitted until the term 2 entry reaches majority, then commitIndex becomes 1.
func TestRaftLogConsistencyIndirectCommit(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	r1.raftTerm = 1
	r1.raftState = raftLeader
	manuallyAppendLog(r1, 1, "cmd", []byte("term1"))

	// r1 replicates to no one and dies.

	// r2 becomes leader for term 2. It inherits the log.
	r2.raftState = raftLeader
	r2.raftTerm = 2
	r2.commitIndex = -1
	for _, id := range []string{r1.ID, r2.ID, r3.ID} {
		r2.nextIndex[id] = 1
		r2.matchIndex[id] = -1
	}
	manuallyAppendLog(r2, 1, "cmd", []byte("term1")) // inherit index 0

	// 1. r2 replicates term1 log to majority (r3)
	// Notice: r2 has not added a term 2 log yet.
	r2.matchIndex[r3.ID] = 0 // manually fake replication response

	r2.mu.Lock()
	r2.advanceCommitIndexLocked()
	r2.mu.Unlock()

	// Raft rules: Leader cannot commit old term entry directly, even if replicated.
	if r2.commitIndex == 0 {
		t.Fatalf("leader should not directly commit an entry from a previous term")
	}

	// 2. r2 appends an entry for its OWN term, and replicates both to majority
	manuallyAppendLog(r2, 2, "cmd", []byte("term2"))
	r2.matchIndex[r3.ID] = 1

	r2.mu.Lock()
	r2.advanceCommitIndexLocked()
	r2.mu.Unlock()

	// Now that an entry from term 2 was committed, index 0 is indirectly committed too.
	if r2.commitIndex != 1 {
		t.Fatalf("leader should commit both after current term entry reaches majority")
	}
}

// Objective: verify a follower advances commitIndex to the minimum of leaderCommit and its last log index.
// Expected output: after heartbeat delivery, the follower commitIndex becomes 1.
func TestRaftLogConsistencyFollowerCommit(t *testing.T) {
	robots := setupCluster(2)
	r1, r2 := robots[0], robots[1]

	electLeader(t, r1, r2)
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("y"))

	// Replicate everything to r2 (without advancing leader commit yet)
	replicateLog(r1, r2)

	// Leader commits index 1
	r1.mu.Lock()
	r1.advanceCommitIndexLocked() // should go to 1
	if r1.commitIndex != 1 {
		t.Fatalf("leader commitIndex should be 1")
	}
	r1.mu.Unlock()

	// Follower's commitIndex is currently -1. Leader sends heartbeat with leaderCommit=1.
	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	r2.HandleAppendEntries(req)

	r2.mu.Lock()
	defer r2.mu.Unlock()
	if r2.commitIndex != 1 {
		t.Fatalf("follower commitIndex should have advanced to min(leaderCommit, lastLogIndex) -> 1")
	}
}
