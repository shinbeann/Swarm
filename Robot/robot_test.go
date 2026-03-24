package main

import (
	"testing"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

const forcedElectionTimeoutDuration = 20 * time.Second

func buildConditions(self string, peers ...string) []*pb.NetworkData {
	conds := make([]*pb.NetworkData, 0, len(peers))
	for _, p := range peers {
		if p == self {
			continue
		}
		conds = append(conds, &pb.NetworkData{
			TargetRobotId: p,
			Bandwidth:     100,
			Latency:       1,
			Reliability:   1,
		})
	}
	return conds
}

func forceElectionTimeout(r *Robot) {
	r.mu.Lock()
	r.lastLeaderSeenAt = time.Now().Add(-forcedElectionTimeoutDuration)
	r.electionTimeout = time.Millisecond
	r.mu.Unlock()
}

func TestRaftElectionFailoverAndPeriodicPing(t *testing.T) {
	r1 := NewRobot("r1", nil)
	r2 := NewRobot("r2", nil)
	r3 := NewRobot("r3", nil)

	r1.updatePeerTopology(buildConditions("r1", "r2", "r3"))
	r2.updatePeerTopology(buildConditions("r2", "r1", "r3"))
	r3.updatePeerTopology(buildConditions("r3", "r1", "r2"))

	// Initial election: r1 should become leader after collecting votes.
	forceElectionTimeout(r1)
	r1.maybeStartElection()
	if r1.raftState != raftCandidate {
		t.Fatalf("r1 expected candidate, got %v", r1.raftState)
	}

	voteReq := r1.buildVoteRequest()
	if voteReq == nil {
		t.Fatalf("expected vote request from candidate")
	}

	respFromR2 := r2.HandleRequestVote(voteReq)
	respFromR3 := r3.HandleRequestVote(voteReq)
	r1.handleVoteResponse("r2", respFromR2)
	r1.handleVoteResponse("r3", respFromR3)

	if r1.raftState != raftLeader {
		t.Fatalf("r1 expected leader after majority votes, got %v", r1.raftState)
	}
	leaderTerm := r1.raftTerm

	// Leader should emit append-entry ping with timestamp and then heartbeat until 10s interval.
	r1.mu.Lock()
	r1.maybeAppendLeaderPingLocked(time.Now())
	r1.mu.Unlock()

	appendMsg := r1.buildAppendEntriesRequestForPeer("r2")
	if appendMsg == nil || len(appendMsg.GetEntries()) == 0 {
		t.Fatalf("expected append entry ping from leader")
	}
	if appendMsg.GetEntries()[0].GetTimestampUnixMs() <= 0 {
		t.Fatalf("expected timestamp on leader ping append entry")
	}
	if appendMsg.GetEntries()[0].GetLogType() != "leader_ping" {
		t.Fatalf("expected leader_ping log type, got %s", appendMsg.GetEntries()[0].GetLogType())
	}

	appendResp := r2.HandleAppendEntries(appendMsg)
	r1.handleAppendResponse("r2", appendMsg, appendResp)

	heartbeatMsg := r1.buildAppendEntriesRequestForPeer("r2")
	if heartbeatMsg == nil || len(heartbeatMsg.GetEntries()) != 0 {
		t.Fatalf("expected heartbeat before 10s ping interval")
	}

	r1.mu.Lock()
	r1.lastRaftPingSentAt = time.Now().Add(-11 * time.Second)
	r1.mu.Unlock()

	r1.mu.Lock()
	r1.maybeAppendLeaderPingLocked(time.Now())
	r1.mu.Unlock()

	appendMsg2 := r1.buildAppendEntriesRequestForPeer("r3")
	if appendMsg2 == nil || len(appendMsg2.GetEntries()) == 0 {
		t.Fatalf("expected periodic append entry after ping interval")
	}
	if len(appendMsg2.GetEntries()) <= len(appendMsg.GetEntries()) {
		t.Fatalf("expected additional replicated entries on second append")
	}

	// Failover: simulate r1 unavailable and elect r2 with votes from self + r3.
	forceElectionTimeout(r2)
	r2.maybeStartElection()
	if r2.raftState != raftCandidate {
		t.Fatalf("r2 expected candidate during failover, got %v", r2.raftState)
	}

	voteReq2 := r2.buildVoteRequest()
	if voteReq2 == nil {
		t.Fatalf("expected vote request from r2")
	}

	respFromR3ForR2 := r3.HandleRequestVote(voteReq2)
	r2.handleVoteResponse("r3", respFromR3ForR2)

	if r2.raftState != raftLeader {
		t.Fatalf("r2 expected leader after failover majority, got %v", r2.raftState)
	}
	if r2.raftTerm <= leaderTerm {
		t.Fatalf("expected failover term to advance, old=%d new=%d", leaderTerm, r2.raftTerm)
	}
}
