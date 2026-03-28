package main

import (
	"bytes"
	"testing"
)

// BL means Becoming Leader
// L means Leader

// BL1/BL2: nextIndex and matchIndex initialization
func TestRaftLeaderInitIndices(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	manuallyAppendLog(r1, 1, "cmd", []byte("x"))
	manuallyAppendLog(r1, 1, "cmd", []byte("y"))

	forceElectionTimeout(r1)
	r1.maybeStartElection()

	req := r1.buildVoteRequest()
	r1.handleVoteResponse(r2.ID, r2.HandleRequestVote(req))
	r1.handleVoteResponse(r3.ID, r3.HandleRequestVote(req))

	if r1.raftState != raftLeader {
		t.Fatalf("expected r1 to be leader")
	}

	r1.mu.Lock()
	defer r1.mu.Unlock()

	expectedNext := int64(2) // length of log
	if r1.nextIndex[r2.ID] != expectedNext || r1.nextIndex[r3.ID] != expectedNext {
		t.Fatalf("expected nextIndex to be %d for peers", expectedNext)
	}
	if r1.matchIndex[r2.ID] != -1 || r1.matchIndex[r3.ID] != -1 {
		t.Fatalf("expected matchIndex to be -1 for peers")
	}
	if r1.matchIndex[r1.ID] != 1 {
		t.Fatalf("expected matchIndex for self to be last log index (1), got %d", r1.matchIndex[r1.ID])
	}
}

// L1: Heartbeat to in-sync follower
func TestRaftLeaderHeartbeatInSync(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	replicateLog(r1, r2) // r2 is now in sync

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	// heartbeat should have 0 entries
	if len(req.GetEntries()) != 0 {
		t.Fatalf("expected heartbeat to have 0 entries")
	}

	resp := r2.HandleAppendEntries(req)
	if !resp.GetSuccess() {
		t.Fatalf("expected follower to accept heartbeat")
	}
}

// L2: Heartbeat to out-of-sync follower
func TestRaftLeaderHeartbeatOutOfSync(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("y"))

	// Force r1 to think nextIndex for r2 is 2 (as if r2 has index 0 and 1)
	// But r2 actually has an empty log
	r1.mu.Lock()
	r1.nextIndex[r2.ID] = 2
	r1.mu.Unlock()

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	// r1 sends prevLogIndex = 1, prevLogTerm = 1
	// r2 has empty log, so it will reject

	resp := r2.HandleAppendEntries(req)
	if resp.GetSuccess() {
		t.Fatalf("expected out-of-sync follower to reject append")
	}
}

// L3: Backtracking correction
func TestRaftLeaderBacktracking(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3)

	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("val1"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("val2"))
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("val3"))

	// Simulate r2 being dead during these appends, so r1's nextIndex for r2 is 3
	r1.mu.Lock()
	r1.nextIndex[r2.ID] = 3
	r1.mu.Unlock()

	// 1. Leader attempts to append from index 3 (prevLogIndex=2). Follower rejects.
	req1 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp1 := r2.HandleAppendEntries(req1)
	r1.handleAppendResponse(r2.ID, req1, resp1)

	if resp1.GetSuccess() {
		t.Fatalf("expected rejection 1")
	}

	// 2. Leader backtracks nextIndex to 2, attempts again (prevLogIndex=1). Follower rejects.
	req2 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp2 := r2.HandleAppendEntries(req2)
	r1.handleAppendResponse(r2.ID, req2, resp2)

	if resp2.GetSuccess() {
		t.Fatalf("expected rejection 2")
	}

	// 3. Leader backtracks nextIndex to 1, attempts again (prevLogIndex=0). Follower rejects.
	req3 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp3 := r2.HandleAppendEntries(req3)
	r1.handleAppendResponse(r2.ID, req3, resp3)

	if resp3.GetSuccess() {
		t.Fatalf("expected rejection 3")
	}

	// 4. Leader backtracks nextIndex to 0, attempts again (prevLogIndex=-1). Follower ACCEPTS.
	req4 := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp4 := r2.HandleAppendEntries(req4)
	r1.handleAppendResponse(r2.ID, req4, resp4)

	if !resp4.GetSuccess() {
		t.Fatalf("expected acceptance at prevLogIndex=-1")
	}

	// Verify follower log
	r2.mu.Lock()
	defer r2.mu.Unlock()
	if len(r2.raftLog) != 3 {
		t.Fatalf("expected follower to copy all 3 logs, got %d", len(r2.raftLog))
	}
	if !bytes.Equal(r2.raftLog[2].Payload, []byte("val3")) {
		t.Fatalf("expected last log to be val3")
	}
}
