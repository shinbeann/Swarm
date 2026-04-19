package main

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/yihre/swarm-project/communications/proto"
)

func countLogEntriesOfType(robot *Robot, entryType string) int {
	count := 0
	for _, entry := range robot.raftLog {
		if entry.Type == entryType {
			count++
		}
	}
	return count
}

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

// Objective: verify a leader appends a pending casualty verification once and only once.
// Expected output: the first leader tick appends one casualty_verified entry, and later ticks do not add duplicates.
func TestLeaderAppendsPendingCasualtyVerificationOnce(t *testing.T) {
	leader := NewRobot("leader", nil)
	leader.raftState = raftLeader
	leader.raftTerm = 1

	id := LandmarkID("casualty-once")
	loc := Location{X: 180, Y: 240}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-1", 1); verified {
		t.Fatalf("expected first report to stay unverified")
	}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-2", 2); verified {
		t.Fatalf("expected second report to stay unverified")
	}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-3", 3); !verified {
		t.Fatalf("expected third report to signal quorum")
	}

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, "casualty_verified"); got != 1 {
		t.Fatalf("expected exactly one casualty_verified entry after first leader tick, got %d", got)
	}

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, "casualty_verified"); got != 1 {
		t.Fatalf("expected no duplicate casualty_verified entries on later leader ticks, got %d", got)
	}
}

// Objective: verify a committed casualty is not appended again after leadership changes.
// Expected output: a future leader that already has the committed casualty in its store does not add a second casualty_verified entry.
func TestCommittedCasualtyIsNotRecommittedByFutureLeader(t *testing.T) {
	leader := NewRobot("leader", nil)
	leader.raftState = raftLeader
	leader.raftTerm = 1

	id := LandmarkID("casualty-future-leader")
	loc := Location{X: 220, Y: 280}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-1", 1); verified {
		t.Fatalf("expected first report to stay unverified")
	}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-2", 2); verified {
		t.Fatalf("expected second report to stay unverified")
	}
	if verified := leader.store.Add(id, LandmarkCasualty, loc, "peer-3", 3); !verified {
		t.Fatalf("expected third report to signal quorum")
	}

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, "casualty_verified"); got != 1 {
		t.Fatalf("expected one casualty_verified entry before commit, got %d", got)
	}

	leader.mu.Lock()
	leader.commitIndex = int64(len(leader.raftLog) - 1)
	leader.applyCommittedEntriesLocked()
	leader.mu.Unlock()

	futureLeader := NewRobot("future-leader", nil)
	futureLeader.raftTerm = 1
	futureLeader.raftState = raftFollower

	committedSnapshot := &LandmarkEntry{
		ID:       id,
		Type:     LandmarkCasualty,
		Location: loc,
		Reporters: map[RobotID]int{
			"peer-1": 1,
			"peer-2": 2,
			"peer-3": 3,
		},
	}
	payload, err := json.Marshal(committedSnapshot)
	if err != nil {
		t.Fatalf("failed to marshal committed snapshot: %v", err)
	}

	appendResp := futureLeader.HandleAppendEntries(&pb.AppendEntriesRequest{
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
		LeaderCommit: 0,
	})
	if !appendResp.GetSuccess() {
		t.Fatalf("expected future leader append to succeed")
	}

	if got := countLogEntriesOfType(futureLeader, "casualty_verified"); got != 1 {
		t.Fatalf("expected the committed casualty to exist once on the future leader, got %d", got)
	}
	entry := findEntry(t, futureLeader.store, id)
	if !entry.Verified {
		t.Fatalf("expected future leader to have the casualty marked verified after commit apply")
	}

	futureLeader.raftState = raftLeader
	futureLeader.syncRaftWithPeers(context.Background())

	if got := countLogEntriesOfType(futureLeader, "casualty_verified"); got != 1 {
		t.Fatalf("expected future leader not to recommit the same casualty, got %d casualty_verified entries", got)
	}
	if len(futureLeader.raftLog) != 1 {
		t.Fatalf("expected no extra raft entries to be added, got %d", len(futureLeader.raftLog))
	}
}
