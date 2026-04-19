package main

import (
	"testing"
)

// Objective: verify a leader demotes when a response reveals a higher term.
// Expected output: r1 becomes follower and updates its term to match r2.
func TestRaftTermSafetyLeaderDemotionOnResponse(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3) // term 1

	// Simulate r2 having moved to a higher term
	r2.mu.Lock()
	r2.raftTerm = r1.raftTerm + 1
	r2.mu.Unlock()

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp := r2.HandleAppendEntries(req) // Should reject and return term 2
	
	if resp.GetSuccess() {
		t.Fatalf("expected r2 to reject append from lower term leader")
	}
	if resp.GetTerm() != r1.raftTerm+1 {
		t.Fatalf("expected response term to be %d", r1.raftTerm+1)
	}

	r1.handleAppendResponse(r2.ID, req, resp)

	r1.mu.Lock()
	state1 := r1.raftState
	term1 := r1.raftTerm
	r1.mu.Unlock()

	if state1 != raftFollower {
		t.Fatalf("expected r1 to demote to follower after seeing higher term in response")
	}
	if term1 != r2.raftTerm {
		t.Fatalf("expected r1 to adopt the higher term %d, got %d", r2.raftTerm, term1)
	}
}

// Objective: verify a leader demotes when receiving a higher-term vote request.
// Expected output: r1 grants the vote, becomes follower, and adopts the newer term.
func TestRaftTermSafetyLeaderDemotionOnVoteRequest(t *testing.T) {
	robots := setupCluster(3)
	r1, r2, r3 := robots[0], robots[1], robots[2]

	electLeader(t, r1, r2, r3) // term 1

	forceElectionTimeout(r2)
	r2.maybeStartElection()

	req := r2.buildVoteRequest()
	resp := r1.HandleRequestVote(req)

	if !resp.GetVoteGranted() {
		t.Fatalf("r1 should grant vote to r2 for higher term")
	}

	r1.mu.Lock()
	state1 := r1.raftState
	term1 := r1.raftTerm
	r1.mu.Unlock()

	if state1 != raftFollower {
		t.Fatalf("expected r1 to demote to follower after receiving vote request with higher term")
	}
	if term1 != r2.raftTerm {
		t.Fatalf("expected r1 to adopt the higher term %d, got %d", r2.raftTerm, term1)
	}
}

// Objective: verify a node rejects AppendEntries messages from a lower-term leader.
// Expected output: the append response is unsuccessful and includes the follower's current term.
func TestRaftTermSafetyRejectLowerTermAppend(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	r1.mu.Lock()
	r1.raftState = raftLeader
	r1.raftTerm = 1
	r1.mu.Unlock()

	r2.mu.Lock()
	r2.raftState = raftFollower
	r2.raftTerm = 2
	r2.mu.Unlock()

	req := r1.buildAppendEntriesRequestForPeer(r2.ID)
	resp := r2.HandleAppendEntries(req)

	if resp.GetSuccess() {
		t.Fatalf("r2 should reject append from term 1 because it is in term 2")
	}
	if resp.GetTerm() != 2 {
		t.Fatalf("r2 response should include its term 2")
	}
}

// Objective: verify a node rejects vote requests from a lower-term candidate.
// Expected output: the vote is denied and the response includes the follower's current term.
func TestRaftTermSafetyRejectLowerTermVote(t *testing.T) {
	robots := setupCluster(3)
	r1, r2 := robots[0], robots[1]

	r1.mu.Lock()
	r1.raftState = raftCandidate
	r1.raftTerm = 1
	r1.mu.Unlock()

	r2.mu.Lock()
	r2.raftState = raftFollower
	r2.raftTerm = 2
	r2.mu.Unlock()

	req := r1.buildVoteRequest()
	resp := r2.HandleRequestVote(req)

	if resp.GetVoteGranted() {
		t.Fatalf("r2 should reject vote for lower term candidate")
	}
	if resp.GetTerm() != 2 {
		t.Fatalf("r2 response should include its term 2")
	}
}
