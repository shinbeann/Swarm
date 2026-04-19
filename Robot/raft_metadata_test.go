package main

import "testing"

func TestLastRaftLogIndexLocked(t *testing.T) {
	r := NewRobot("r1", &fakeRobotServiceClient{})
	t.Cleanup(r.Stop)

	r.mu.Lock()
	if got := r.lastRaftLogIndexLocked(); got != -1 {
		r.mu.Unlock()
		t.Fatalf("expected empty raft log index to be -1, got %d", got)
	}
	r.raftLog = append(r.raftLog, RaftLogEntry{Index: 7}, RaftLogEntry{Index: 15})
	if got := r.lastRaftLogIndexLocked(); got != 15 {
		r.mu.Unlock()
		t.Fatalf("expected latest raft log index 15, got %d", got)
	}
	r.mu.Unlock()
}
