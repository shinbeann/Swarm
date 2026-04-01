package main

import (
	"bytes"
	"testing"
)

// F1: Majority replicated but uncommitted logs
func TestRaftLeaderFailureMajorityReplicatedUncommitted(t *testing.T) {
	robots := setupCluster(5)
	r1, r2, r3, r4, r5 := robots[0], robots[1], robots[2], robots[3], robots[4]

	// 1. r1 becomes leader
	electLeader(t, r1, r2, r3, r4, r5)

	// 2. r1 appends a command and replicates to r2 and r3 (majority: r1, r2, r3)
	cmd := []byte("majority_cmd")
	manuallyAppendLog(r1, r1.raftTerm, "cmd", cmd) // index 0

	replicateLog(r1, r2)
	replicateLog(r1, r3)

	// r1 has committed locally because it reached majority
	if r1.commitIndex != 0 {
		t.Fatalf("expected leader's commitIndex to be 0")
	}

	// followers don't know it's committed yet
	if r2.commitIndex != -1 {
		t.Fatalf("expected follower's commitIndex to be -1")
	}

	// 3. r1 fails. r2 times out and starts election.
	// r2 has the log, so it can win votes from r3, r4, r5.
	electLeader(t, r2, r3, r4, r5)

	// 4. r2 verifies it has the log from r1
	if len(r2.raftLog) != 1 || !bytes.Equal(r2.raftLog[0].Payload, cmd) {
		t.Fatalf("expected r2 to inherit log from previous term")
	}

	// 5. r2 commits an entry from its OWN term (term 2)
	// In Raft, a leader cannot directly commit entries from an older term.
	// It must commit an entry from its current term, which indirectly commits all preceding entries.
	newCmd := []byte("new_leader_ping")
	manuallyAppendLog(r2, r2.raftTerm, "cmd", newCmd) // index 1

	// replicate to majority
	replicateLog(r2, r3)

	// r4 needs to be backtracked because it misses index 0, so
	// a single replicateLog will fail. Keep replicating until success.
	for !replicateLog(r2, r4) {
	}

	// advance commit index on r2
	r2.mu.Lock()
	r2.advanceCommitIndexLocked()
	r2.mu.Unlock()

	// Both index 0 (term 1) and index 1 (term 2) should now be committed
	if r2.commitIndex != 1 {
		t.Fatalf("expected r2 commitIndex to advance to 1, got %d", r2.commitIndex)
	}
}

// F2: Minority replicated logs overwritting
func TestRaftLeaderFailureMinorityOverwritten(t *testing.T) {
	robots := setupCluster(5)
	r1, r2, r3, r4, r5 := robots[0], robots[1], robots[2], robots[3], robots[4]

	electLeader(t, r1, r2, r3, r4, r5) // term 1

	// r1 appends but only replicates to r2 (minority: r1, r2)
	badCmd := []byte("minority_cmd")
	manuallyAppendLog(r1, r1.raftTerm, "cmd", badCmd) // index 0
	replicateLog(r1, r2)

	// r1 fails. r3 starts election and wins (votes from r3, r4, r5)
	// r3's log is empty.
	electLeader(t, r3, r4, r5) // term 2

	// r3 writes a new config/command
	goodCmd := []byte("majority_cmd")
	manuallyAppendLog(r3, r3.raftTerm, "cmd", goodCmd) // index 0

	// r3 replicates to r2
	success := replicateLog(r3, r2)
	if !success {
		t.Fatalf("expected r2 to accept log overwrite from new leader")
	}

	if len(r2.raftLog) != 1 || !bytes.Equal(r2.raftLog[0].Payload, goodCmd) {
		t.Fatalf("expected r2 to overwrite minority log with majority log")
	}
}

// F3: Minority follower has uncommitted logs, becomes leader, and replicates them
func TestRaftLeaderFailureMinorityElectedReplicates(t *testing.T) {
	robots := setupCluster(5)
	r1, r2, r3, r4, r5 := robots[0], robots[1], robots[2], robots[3], robots[4]

	// 1. r1 is elected leader (term 2)
	electLeader(t, r1, r2, r3, r4, r5)

	// 2. r1 appends a log, but only replicates to r2 (minority: r1, r2)
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("x")) // index 0
	replicateLog(r1, r2)

	// 3. r1 fails.
	// r2 has the log (index 0, term 2). r3, r4, r5 do not.
	// r2 times out and starts election for term 3.
	forceElectionTimeout(r2)
	r2.maybeStartElection()
	req := r2.buildVoteRequest()

	// r3, r4, r5 should vote for r2 because its log is more up-to-date
	resp3 := r3.HandleRequestVote(req)
	resp4 := r4.HandleRequestVote(req)
	resp5 := r5.HandleRequestVote(req)

	if !resp3.GetVoteGranted() || !resp4.GetVoteGranted() || !resp5.GetVoteGranted() {
		t.Fatalf("nodes missing logs should vote for candidate with up-to-date logs")
	}

	r2.handleVoteResponse(r3.ID, resp3)
	r2.handleVoteResponse(r4.ID, resp4)
	r2.handleVoteResponse(r5.ID, resp5)

	if r2.raftState != raftLeader {
		t.Fatalf("r2 should become leader")
	}

	// 4. r2 must replicate the uncommitted log to r3 (and others)
	// r2's nextIndex for r3 is 1. It sends an empty heartbeat with prevLogIndex=0, prevLogTerm=2.
	// r3 will reject it because it doesn't have index 0.
	appendReq1 := r2.buildAppendEntriesRequestForPeer(r3.ID)
	appendResp1 := r3.HandleAppendEntries(appendReq1)
	if appendResp1.GetSuccess() {
		t.Fatalf("r3 should reject append because of log mismatch")
	}
	r2.handleAppendResponse(r3.ID, appendReq1, appendResp1)

	// r2 backtracks nextIndex[r3] to 0, sends index 0
	appendReq2 := r2.buildAppendEntriesRequestForPeer(r3.ID)
	if len(appendReq2.GetEntries()) != 1 {
		t.Fatalf("expected r2 to send 1 entry after backtracking")
	}
	appendResp2 := r3.HandleAppendEntries(appendReq2)
	if !appendResp2.GetSuccess() {
		t.Fatalf("r3 should accept the actual log entry")
	}
	r2.handleAppendResponse(r3.ID, appendReq2, appendResp2)

	// 5. In Raft, a leader cannot directly commit entries from previous terms.
	// It must replicate an entry from its CURRENT term to implicitly commit older ones.
	manuallyAppendLog(r2, r2.raftTerm, "cmd", []byte("y")) // index 1, term 3

	// r2 replicates everything to r3 and r4 (loop to handle backtracking)
	for !replicateLog(r2, r3) {
	}
	for !replicateLog(r2, r4) {
	}

	r2.mu.Lock()
	r2.advanceCommitIndexLocked()
	r2.mu.Unlock()

	// Both index 0 (term 2) and index 1 (term 3) should now be committed locally by r2
	if r2.commitIndex != 1 {
		t.Fatalf("expected r2 to commit the old log once it appended and replicated a new log, got %d", r2.commitIndex)
	}
}
