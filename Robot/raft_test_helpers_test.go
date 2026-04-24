package main

import (
	"fmt"
	"testing"
	"time"
)

// forceElectionTimeout artificially backdates the last seen leader time
// and sets a near-zero timeout so that maybeStartElection() will immediately trigger.
func forceElectionTimeout(r *Robot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	// Ensure we are not leader
	r.raftState = raftFollower
	r.lastLeaderSeenAt = time.Now().Add(-1 * time.Hour)
	r.lastElectionAt = time.Time{} // Clear last election to bypass candidate timeout checks
	r.electionTimeout = time.Millisecond
}

// setupCluster creates n robots and fully interconnects their routing tables
// so that updateRaftPeerTopology() will recognize all peers.
func setupCluster(n int) []*Robot {
	robots := make([]*Robot, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%d", i+1)
		robots[i] = NewRobot(id, nil, n)
	}

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			robots[i].routingTable.RecordDirectNeighbour(RobotID(robots[j].ID))
		}
		robots[i].updateRaftPeerTopology()
	}

	return robots
}

// electLeader forces the designated leader to become a candidate, request votes
// from the followers, and asserts that it becomes the leader.
func electLeader(t *testing.T, leader *Robot, followers ...*Robot) {
	forceElectionTimeout(leader)
	leader.maybeStartElection()
	
	leader.mu.Lock()
	state := leader.raftState
	leader.mu.Unlock()
	
	if state != raftCandidate {
		t.Fatalf("expected %s to become candidate, got %v", leader.ID, leader.raftState)
	}

	voteReq := leader.buildVoteRequest()
	if voteReq == nil {
		t.Fatalf("expected vote request from %s", leader.ID)
	}

	for _, f := range followers {
		resp := f.HandleRequestVote(voteReq)
		leader.handleVoteResponse(f.ID, resp)
	}

	leader.mu.Lock()
	state = leader.raftState
	leader.mu.Unlock()

	if state != raftLeader {
		t.Fatalf("expected %s to become leader, got %v", leader.ID, leader.raftState)
	}
}

// manuallyAppendLog forcefully appends a log to the robot's local array
// without routing it through the consensus pipeline.
func manuallyAppendLog(r *Robot, term int64, logType string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := int64(len(r.raftLog))
	r.raftLog = append(r.raftLog, RaftLogEntry{
		Term:            term,
		Index:           idx,
		Type:            logType,
		Payload:         payload,
		TimestampUnixMs: time.Now().UnixMilli(),
	})
	
	// If it's a leader, we should ideally advance matchIndex for self, but usually
	// tests manipulate logs *while* it's a follower or before becoming leader.
	if r.raftState == raftLeader {
		r.matchIndex[r.ID] = idx
	}
}

// replicateLog is a macro to build an AppendEntries from leader for a specific follower,
// handle it on the follower, and route the response back to the leader.
// Returns success status.
func replicateLog(leader *Robot, follower *Robot) bool {
	req := leader.buildAppendEntriesRequestForPeer(follower.ID)
	if req == nil {
		return false
	}
	resp := follower.HandleAppendEntries(req)
	leader.handleAppendResponse(follower.ID, req, resp)
	return resp.GetSuccess()
}
