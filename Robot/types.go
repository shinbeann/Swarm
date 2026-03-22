package main

import "time"

// LandmarkID uniquely identifies a landmark in the world.
type LandmarkID string

// LandmarkType categorizes the landmark.
type LandmarkType string

const (
	LandmarkCasualty LandmarkType = "casualty"
	LandmarkCorridor LandmarkType = "corridor"
	LandmarkObstacle LandmarkType = "obstacle"
)

// Location represents a 2D coordinate.
type Location struct {
	X float64
	Y float64
}

// RobotID uniquely identifies a robot.
type RobotID string

// LandmarkEntry represents a discovered landmark with metadata.
type LandmarkEntry struct {
	ID        LandmarkID
	Type      LandmarkType
	Location  Location
	Reporters map[RobotID]int // robot ID -> Lamport timestamp
	FirstSeen time.Time
}
