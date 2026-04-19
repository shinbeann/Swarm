package main

import (
	"context"
	"testing"

	pb "github.com/yihre/swarm-project/communications/proto"
)

func TestSendHeartbeatStoresRaftMetadata(t *testing.T) {
	s := newServer()

	_, err := s.SendHeartbeat(context.Background(), &pb.HeartbeatRequest{
		RobotId:      "r1",
		X:            100,
		Y:            120,
		Heading:      0.25,
		CurrentTerm:  5,
		LastLogIndex: 15,
		CommitIndex:  14,
	})
	if err != nil {
		t.Fatalf("expected SendHeartbeat to succeed, got error: %v", err)
	}

	s.mu.RLock()
	state := s.robots["r1"]
	s.mu.RUnlock()
	if state == nil || state.Info == nil {
		t.Fatalf("expected robot state for r1 to be initialized")
	}

	if got := state.Info.GetRaftTerm(); got != 5 {
		t.Fatalf("expected raft term 5, got %d", got)
	}
	if got := state.Info.GetRaftLogIndex(); got != 15 {
		t.Fatalf("expected raft log index 15, got %d", got)
	}
	if got := state.Info.GetCommitIndex(); got != 14 {
		t.Fatalf("expected commit index 14, got %d", got)
	}
}

func TestGetRobotDataIncludesRaftMetadata(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	s.robots["r1"] = &RobotState{Info: &pb.RobotInfo{Id: "r1", X: 100, Y: 100, Heading: 0.5, RaftTerm: 5, RaftLogIndex: 15, CommitIndex: 14}, LastKnownLeaderID: "r1"}
	s.robots["r2"] = &RobotState{Info: &pb.RobotInfo{Id: "r2", X: 130, Y: 130, Heading: 0.7, RaftTerm: 5, RaftLogIndex: 11, CommitIndex: 10}, LastKnownLeaderID: "r1"}
	s.mu.Unlock()

	resp, err := s.GetRobotData(context.Background(), &pb.RobotDataRequest{})
	if err != nil {
		t.Fatalf("expected GetRobotData to succeed, got error: %v", err)
	}

	robotsByID := make(map[string]*pb.RobotInfo)
	for _, robot := range resp.GetRobots() {
		robotsByID[robot.GetId()] = robot
	}

	r1, ok := robotsByID["r1"]
	if !ok {
		t.Fatalf("expected r1 in robot response")
	}
	if got := r1.GetRaftTerm(); got != 5 {
		t.Fatalf("expected r1 raft term 5, got %d", got)
	}
	if got := r1.GetRaftLogIndex(); got != 15 {
		t.Fatalf("expected r1 raft log index 15, got %d", got)
	}
	if got := r1.GetCommitIndex(); got != 14 {
		t.Fatalf("expected r1 commit index 14, got %d", got)
	}

	r2, ok := robotsByID["r2"]
	if !ok {
		t.Fatalf("expected r2 in robot response")
	}
	if got := r2.GetRaftLogIndex(); got != 11 {
		t.Fatalf("expected r2 raft log index 11, got %d", got)
	}
	if got := r2.GetCommitIndex(); got != 10 {
		t.Fatalf("expected r2 commit index 10, got %d", got)
	}
}
