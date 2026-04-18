package main

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/yihre/swarm-project/communications/proto"
)

// Objective: verify casualty verification is only recorded once the committed Raft entry is applied.
// Expected output: the store remains unverified before commit and becomes verified after applyCommittedEntriesLocked runs.
func TestRaftCasualtyVerification(t *testing.T) {
	robot1 := NewRobot("robot1", nil)
	robot1.raftState = raftLeader // Force it to be leader

	ctx := context.Background()

	// Wait for tick to execute pending work
	robot1.mu.Lock()
	robot1.mu.Unlock()

	// 1st report
	robot1.store.Add("casualty-1", LandmarkCasualty, Location{X: 10, Y: 10}, "peer1", 1)
	
	// Call syncRaftWithPeers to attempt to run appendVerifiedCasualtiesLocked
	robot1.syncRaftWithPeers(ctx)

	robot1.mu.Lock()
	logsAfterFirst := len(robot1.raftLog)
	robot1.mu.Unlock()

	// 2nd report
	robot1.store.Add("casualty-1", LandmarkCasualty, Location{X: 10, Y: 10}, "peer2", 2)
	robot1.syncRaftWithPeers(ctx)

	// 3rd report -> Verification quorum reached!
	robot1.store.Add("casualty-1", LandmarkCasualty, Location{X: 10, Y: 10}, "peer3", 3)
	robot1.syncRaftWithPeers(ctx)

	entryBeforeCommit := findEntry(t, robot1.store, "casualty-1")
	if entryBeforeCommit.Verified {
		t.Fatalf("expected casualty to remain unverified before committed raft apply")
	}

	robot1.mu.Lock()
	if len(robot1.raftLog) == logsAfterFirst {
		t.Fatalf("expected log length to increase after verification, got %d", len(robot1.raftLog))
	}

	lastEntry := robot1.raftLog[len(robot1.raftLog)-1]
	if lastEntry.Type != "casualty_verified" {
		t.Fatalf("expected log type 'casualty_verified', got %s", lastEntry.Type)
	}

	var entry LandmarkEntry
	if err := json.Unmarshal(lastEntry.Payload, &entry); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if entry.ID != "casualty-1" || entry.Type != LandmarkCasualty {
		t.Fatalf("payload does not match expected ID 'casualty-1' and type 'casualty', got ID %s and type %s", entry.ID, entry.Type)
	}

	robot1.commitIndex = int64(len(robot1.raftLog) - 1)
	robot1.applyCommittedEntriesLocked()
	robot1.mu.Unlock()

	entryAfterCommit := findEntry(t, robot1.store, "casualty-1")
	if !entryAfterCommit.Verified {
		t.Fatalf("expected casualty to be verified only after committed raft apply")
	}
}

// Objective: verify a follower applies casualty verification only after the leader commits the entry.
// Expected output: the append succeeds, the follower stays unverified before commit, and becomes verified after commit.
func TestFollowerAppliesCommittedCasualtyVerification(t *testing.T) {
	follower := NewRobot("follower", nil)
	follower.raftTerm = 1

	snapshot := &LandmarkEntry{
		ID:       "casualty-2",
		Type:     LandmarkCasualty,
		Location: Location{X: 25, Y: 40},
		Reporters: map[RobotID]int{
			"r1": 1,
			"r2": 2,
			"r3": 3,
		},
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}

	appendResp := follower.HandleAppendEntries(&pb.AppendEntriesRequest{
		Term:         1,
		LeaderId:     "leader",
		PrevLogIndex: -1,
		PrevLogTerm:  -1,
		Entries: []*pb.RaftLogEntry{{
			Term:            1,
			Command:         payload,
			LogType:         "casualty_verified",
			TimestampUnixMs: 1,
		}},
		LeaderCommit: -1,
	})
	if !appendResp.GetSuccess() {
		t.Fatalf("expected follower append to succeed")
	}

	for _, e := range follower.store.GetAll() {
		if e.ID == "casualty-2" {
			t.Fatalf("expected follower not to apply casualty before commit")
		}
	}

	heartbeatResp := follower.HandleAppendEntries(&pb.AppendEntriesRequest{
		Term:         1,
		LeaderId:     "leader",
		PrevLogIndex: 0,
		PrevLogTerm:  1,
		Entries:      nil,
		LeaderCommit: 0,
	})
	if !heartbeatResp.GetSuccess() {
		t.Fatalf("expected follower heartbeat append to succeed")
	}

	entry := findEntry(t, follower.store, "casualty-2")
	if !entry.Verified {
		t.Fatalf("expected follower to verify casualty after committed raft apply")
	}
}
