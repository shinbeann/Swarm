package main

import (
	"testing"
	"time"
)

// Objective: verify candidate vote retries are paced by the retry interval.
// Expected output: the first and post-interval vote attempts return true, and the mid-interval attempt returns false.
func TestCandidateVoteRetryPacing(t *testing.T) {
	r := NewRobot("r1", nil)
	defer r.Stop()

	start := time.Now()

	r.mu.Lock()
	r.raftState = raftCandidate

	if !r.shouldSendCandidateVotesLocked(start) {
		r.mu.Unlock()
		t.Fatalf("expected first candidate vote round to send immediately")
	}

	if r.shouldSendCandidateVotesLocked(start.Add(raftCandidateVoteRetry / 2)) {
		r.mu.Unlock()
		t.Fatalf("expected candidate vote round to be throttled before retry interval")
	}

	if !r.shouldSendCandidateVotesLocked(start.Add(raftCandidateVoteRetry + time.Millisecond)) {
		r.mu.Unlock()
		t.Fatalf("expected candidate vote round to send after retry interval")
	}
	r.mu.Unlock()
}

// Objective: verify a candidate-to-follower transition clears the vote retry timer.
// Expected output: the follower transition resets lastVoteRequestAt to zero.
func TestCandidateVoteRetryResetOnFollowerTransition(t *testing.T) {
	r := NewRobot("r1", nil)
	defer r.Stop()

	r.mu.Lock()
	r.raftState = raftCandidate
	if !r.shouldSendCandidateVotesLocked(time.Now()) {
		r.mu.Unlock()
		t.Fatalf("expected initial candidate vote round to send")
	}
	r.becomeFollowerLocked(r.raftTerm+1, "")
	if !r.lastVoteRequestAt.IsZero() {
		r.mu.Unlock()
		t.Fatalf("expected candidate vote retry timer to reset after follower transition")
	}
	r.mu.Unlock()
}
