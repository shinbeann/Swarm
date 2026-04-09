package main

import (
	"context"
	"encoding/json"
	"testing"
)

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
	robot1.mu.Unlock()
}
