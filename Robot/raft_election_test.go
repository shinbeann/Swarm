package main

import (
	"testing"
	"time"
)

// E1: Basic stability test
func TestRaftElectionBasic(t *testing.T) {
	robots := setupCluster(5)
	r1, r2, r3, r4 := robots[0], robots[1], robots[2], robots[3]

	// 1. Force r1 to start election
	forceElectionTimeout(r1)
	r1.maybeStartElection()

	if r1.raftState != raftCandidate {
		t.Fatalf("expected r1 to be candidate")
	}

	voteReq := r1.buildVoteRequest()
	resp2 := r2.HandleRequestVote(voteReq)
	resp3 := r3.HandleRequestVote(voteReq)
	
	r1.handleVoteResponse(r2.ID, resp2)
	r1.handleVoteResponse(r3.ID, resp3)

	if r1.raftState != raftLeader {
		t.Fatalf("expected r1 to be leader after 3 votes (including self)")
	}

	// 2. Verify others remain followers when receiving heartbeats
	appendReq := r1.buildAppendEntriesRequestForPeer(r4.ID)
	resp4 := r4.HandleAppendEntries(appendReq)
	if !resp4.GetSuccess() {
		t.Fatalf("expected heartbeat to succeed")
	}

	r4.mu.Lock()
	state4 := r4.raftState
	lastLeaderSeen := r4.lastLeaderSeenAt
	r4.mu.Unlock()

	if state4 != raftFollower {
		t.Fatalf("expected r4 to remain follower")
	}
	if time.Since(lastLeaderSeen) > time.Second {
		t.Fatalf("expected lastLeaderSeenAt to be recently updated by heartbeat")
	}
}

// E2: Stale leader demotion
func TestRaftStaleLeaderDemotion(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	// r1 is leader of term 1
	r1.raftTerm = 1
	r1.raftState = raftLeader

	// Network split, r2 and r3 elect r2 for term 2
	r2.raftTerm = 2
	r2.raftState = raftLeader

	// r1 (stale) sends heartbeat to r2
	appendReq := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp2 := r2.HandleAppendEntries(appendReq)

	// r2 should reject because term 1 < term 2
	if resp2.GetSuccess() {
		t.Fatalf("expected stale heartbeat to be rejected")
	}

	// r1 processes the response with term 2
	r1.handleAppendResponse(r2.ID, appendReq, resp2)

	r1.mu.Lock()
	state1 := r1.raftState
	term1 := r1.raftTerm
	r1.mu.Unlock()

	if state1 != raftFollower {
		t.Fatalf("expected r1 to demote to follower, got %v", state1)
	}
	if term1 != 2 {
		t.Fatalf("expected r1 to update to term 2, got %v", term1)
	}
}

// E3: Stale log rejection
func TestRaftElectionStaleLogRejection(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	// Give r2 and r3 a longer log (from term 1)
	manuallyAppendLog(r2, 1, "cmd", []byte("x"))
	manuallyAppendLog(r3, 1, "cmd", []byte("x"))

	// r1 has an empty log but tries to start an election for term 2
	forceElectionTimeout(r1)
	r1.maybeStartElection() // r1 term becomes 2

	voteReq := r1.buildVoteRequest()
	resp2 := r2.HandleRequestVote(voteReq)

	if resp2.GetVoteGranted() {
		t.Fatalf("r2 should reject vote for r1 due to stale log")
	}

	r1.handleVoteResponse(r2.ID, resp2)
	
	if r1.raftState == raftLeader {
		t.Fatalf("r1 should not become leader")
	}
}

// E4: Split vote resolution
func TestRaftSplitVote(t *testing.T) {
	robots := setupCluster(4)
	r1, r2, r3, r4 := robots[0], robots[1], robots[2], robots[3]

	// r1 and r2 start elections simultaneously
	forceElectionTimeout(r1)
	forceElectionTimeout(r2)
	r1.maybeStartElection() // term 2
	r2.maybeStartElection() // term 2

	req1 := r1.buildVoteRequest()
	req2 := r2.buildVoteRequest()

	// r3 votes for r1
	resp3_1 := r3.HandleRequestVote(req1)
	r1.handleVoteResponse(r3.ID, resp3_1)

	// r4 votes for r2
	resp4_2 := r4.HandleRequestVote(req2)
	r2.handleVoteResponse(r4.ID, resp4_2)

	// Neither should be leader (2 votes each out of 4)
	if r1.raftState == raftLeader || r2.raftState == raftLeader {
		t.Fatalf("neither r1 nor r2 should be leader during split vote")
	}

	// r1 times out again and starts a new election for term 3
	forceElectionTimeout(r1)
	r1.maybeStartElection()

	req1_new := r1.buildVoteRequest()
	resp2_new := r2.HandleRequestVote(req1_new)
	resp3_new := r3.HandleRequestVote(req1_new) // r3 already voted for r1 in term 2, but term 3 is new

	r1.handleVoteResponse(r2.ID, resp2_new)
	r1.handleVoteResponse(r3.ID, resp3_new)

	if r1.raftState != raftLeader {
		t.Fatalf("r1 should win election in new term")
	}
}

// E5: Candidate step-down on AppendEntries
func TestRaftCandidateStepDownOnAppendEntries(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	// Set r1 up as a candidate for term 2
	forceElectionTimeout(r1)
	r1.maybeStartElection()

	if r1.raftState != raftCandidate {
		t.Fatalf("expected r1 to be candidate")
	}

	// r2 magically becomes leader for term 2 (simulating resolving split vote)
	r2.raftTerm = 2
	r2.raftState = raftLeader

	// r2 sends heartbeat to r1
	appendReq := r2.buildAppendEntriesRequestForPeer(r1.ID)
	resp := r1.HandleAppendEntries(appendReq)

	if !resp.GetSuccess() {
		t.Fatalf("r1 should accept heartbeat from leader of same term")
	}

	if r1.raftState != raftFollower {
		t.Fatalf("r1 should have stepped down from candidate to follower")
	}
}

// E6: Candidate step-down on higher-term VoteRequest
func TestRaftCandidateStepDownOnHigherTermVoteRequest(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	// r1 is candidate for term 2
	forceElectionTimeout(r1)
	r1.maybeStartElection()

	// r2 starts election for term 3
	forceElectionTimeout(r2)
	r2.raftTerm = 2 // advance slowly
	r2.maybeStartElection() // term becomes 3

	req2 := r2.buildVoteRequest()
	
	// r1 receives vote request from r2 (term 3)
	resp := r1.HandleRequestVote(req2)
	
	if !resp.GetVoteGranted() {
		t.Fatalf("r1 should grant vote to r2 for higher term")
	}

	if r1.raftState != raftFollower {
		t.Fatalf("r1 should step down from candidate after seeing higher term")
	}
	if r1.raftTerm != 3 {
		t.Fatalf("r1 term should advance to 3")
	}
}

// E7: Double-vote prevention
func TestRaftDoubleVotePrevention(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	// r1 and r2 both candidates for term 2
	forceElectionTimeout(r1)
	forceElectionTimeout(r2)
	r1.maybeStartElection()
	r2.maybeStartElection()

	req1 := r1.buildVoteRequest()
	req2 := r2.buildVoteRequest()

	// r3 receives request from r1 first
	resp1 := r3.HandleRequestVote(req1)
	if !resp1.GetVoteGranted() {
		t.Fatalf("r3 should grant vote to r1")
	}

	// r3 receives request from r2 for the same term
	resp2 := r3.HandleRequestVote(req2)
	if resp2.GetVoteGranted() {
		t.Fatalf("r3 should NOT grant vote to r2 for the same term")
	}
}
