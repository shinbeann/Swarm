package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

func TestSetNetworkPartitionFiltersNetworkAndVisualiserPeers(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	now := time.Now()
	s.robots["r1"] = &RobotState{Info: &pb.RobotInfo{Id: "r1", X: 100, Y: 100}, LastSeen: now}
	s.robots["r2"] = &RobotState{Info: &pb.RobotInfo{Id: "r2", X: 120, Y: 100}, LastSeen: now}
	s.robots["r3"] = &RobotState{Info: &pb.RobotInfo{Id: "r3", X: 140, Y: 100}, LastSeen: now}
	s.partitionGroupByRobotID["r1"] = 1
	s.partitionGroupByRobotID["r2"] = 1
	s.partitionGroupByRobotID["r3"] = 2
	s.mu.Unlock()

	networkResp, err := s.GetNetworkData(context.Background(), &pb.NetworkRequest{RobotId: "r1"})
	if err != nil {
		t.Fatalf("expected GetNetworkData to succeed, got error: %v", err)
	}

	if len(networkResp.GetNetworkConditions()) != 1 {
		t.Fatalf("expected only one same-group peer in network conditions, got %d", len(networkResp.GetNetworkConditions()))
	}
	if got := networkResp.GetNetworkConditions()[0].GetTargetRobotId(); got != "r2" {
		t.Fatalf("expected r1 to see r2 only, got %s", got)
	}

	robotResp, err := s.GetRobotData(context.Background(), &pb.RobotDataRequest{})
	if err != nil {
		t.Fatalf("expected GetRobotData to succeed, got error: %v", err)
	}

	robotsByID := make(map[string]*pb.RobotInfo, len(robotResp.GetRobots()))
	for _, robot := range robotResp.GetRobots() {
		robotsByID[robot.GetId()] = robot
	}

	r1, ok := robotsByID["r1"]
	if !ok {
		t.Fatalf("expected r1 in robot response")
	}
	if len(r1.GetInRangePeerIds()) != 1 || r1.GetInRangePeerIds()[0] != "r2" {
		t.Fatalf("expected r1 in-range peers to contain only r2, got %v", r1.GetInRangePeerIds())
	}
}

func TestSetNetworkPartitionValidation(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	now := time.Now()
	s.robots["r1"] = &RobotState{Info: &pb.RobotInfo{Id: "r1", X: 200, Y: 200}, LastSeen: now}
	s.partitionGroupByRobotID["r1"] = 1
	s.mu.Unlock()

	invalidGroupResp, err := s.SetNetworkPartition(context.Background(), &pb.NetworkPartitionRequest{
		Assignments: []*pb.PartitionAssignment{{RobotId: "r1", GroupIndex: 4}},
	})
	if err != nil {
		t.Fatalf("expected invalid group call to return response without transport error, got %v", err)
	}
	if invalidGroupResp.GetSuccess() {
		t.Fatalf("expected invalid group assignment to fail")
	}

	unknownRobotResp, err := s.SetNetworkPartition(context.Background(), &pb.NetworkPartitionRequest{
		Assignments: []*pb.PartitionAssignment{{RobotId: "missing", GroupIndex: 2}},
	})
	if err != nil {
		t.Fatalf("expected unknown robot call to return response without transport error, got %v", err)
	}
	if unknownRobotResp.GetSuccess() {
		t.Fatalf("expected unknown robot assignment to fail")
	}
}
