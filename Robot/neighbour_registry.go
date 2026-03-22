package main

import (
	"sync"
	"time"
)

// NeighbourRegistry tracks active neighbours based on heartbeats.
type NeighbourRegistry struct {
	mu       sync.RWMutex
	lastSeen map[RobotID]time.Time
	timeout  time.Duration
}

func NewNeighbourRegistry(timeout time.Duration) *NeighbourRegistry {
	return &NeighbourRegistry{
		lastSeen: make(map[RobotID]time.Time),
		timeout:  timeout,
	}
}

func (nr *NeighbourRegistry) RecordHeartbeat(id RobotID) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.lastSeen[id] = time.Now()
}

func (nr *NeighbourRegistry) GetActive() []RobotID {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	now := time.Now()
	active := make([]RobotID, 0, len(nr.lastSeen))
	for id, ts := range nr.lastSeen {
		if now.Sub(ts) <= nr.timeout {
			active = append(active, id)
		} else {
			delete(nr.lastSeen, id)
		}
	}
	return active
}
