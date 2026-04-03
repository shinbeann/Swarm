package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

func TestGetSensorDataReturnsLandmarkWhenRobotIsWithinRange(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	s.robots["scout"] = &RobotState{
		Info: &pb.RobotInfo{
			Id:      "scout",
			X:       191,
			Y:       200,
			Heading: 0,
		},
	}
	s.mu.Unlock()

	resp, err := s.GetSensorData(context.Background(), &pb.SensorRequest{RobotId: "scout"})
	if err != nil {
		t.Fatalf("expected GetSensorData to succeed, got error: %v", err)
	}

	objects := resp.GetObjects()
	if len(objects) != 1 {
		t.Fatalf("expected exactly one sensed landmark, got %d", len(objects))
	}

	obj := objects[0]
	if obj.GetId() != "casualty-3" {
		t.Fatalf("expected casualty-3 to be sensed, got %s", obj.GetId())
	}
	if obj.GetType() != "landmark:casualty" {
		t.Fatalf("expected sensed object type to be landmark:casualty, got %s", obj.GetType())
	}
	if obj.GetX() != 200 || obj.GetY() != 200 {
		t.Fatalf("expected sensed landmark coordinates to be (200, 200), got (%.1f, %.1f)", obj.GetX(), obj.GetY())
	}
}

func TestGetSensorDataOmitsLandmarkWhenRobotIsOutsideRange(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	s.robots["scout"] = &RobotState{
		Info: &pb.RobotInfo{
			Id:      "scout",
				X:       301,
			Y:       200,
			Heading: 0,
		},
	}
	s.mu.Unlock()

	resp, err := s.GetSensorData(context.Background(), &pb.SensorRequest{RobotId: "scout"})
	if err != nil {
		t.Fatalf("expected GetSensorData to succeed, got error: %v", err)
	}

	if len(resp.GetObjects()) != 0 {
		t.Fatalf("expected no landmarks outside sensor range, got %d objects", len(resp.GetObjects()))
	}
}

func TestGetSensorDataAppliesLandmarkCooldown(t *testing.T) {
	s := newServer()

	s.mu.Lock()
	s.robots["scout"] = &RobotState{
		Info: &pb.RobotInfo{
			Id:      "scout",
			X:       191,
			Y:       200,
			Heading: 0,
		},
	}
	s.mu.Unlock()

	first, err := s.GetSensorData(context.Background(), &pb.SensorRequest{RobotId: "scout"})
	if err != nil {
		t.Fatalf("expected first GetSensorData call to succeed, got error: %v", err)
	}
	if len(first.GetObjects()) != 1 {
		t.Fatalf("expected first sensor read to include the landmark, got %d objects", len(first.GetObjects()))
	}

	second, err := s.GetSensorData(context.Background(), &pb.SensorRequest{RobotId: "scout"})
	if err != nil {
		t.Fatalf("expected second GetSensorData call to succeed, got error: %v", err)
	}
	if len(second.GetObjects()) != 0 {
		t.Fatalf("expected repeated sensor read to be suppressed by cooldown, got %d objects", len(second.GetObjects()))
	}

	s.mu.Lock()
	s.lastLandmarkSensorReport["scout"][LandmarkID("casualty-3")] = time.Now().Add(-landmarkSensorCooldown - time.Second)
	s.mu.Unlock()

	third, err := s.GetSensorData(context.Background(), &pb.SensorRequest{RobotId: "scout"})
	if err != nil {
		t.Fatalf("expected third GetSensorData call to succeed, got error: %v", err)
	}
	if len(third.GetObjects()) != 1 {
		t.Fatalf("expected landmark to be visible again after cooldown, got %d objects", len(third.GetObjects()))
	}
}