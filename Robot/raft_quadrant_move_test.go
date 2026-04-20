package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

// Objective: verify the leader appends exactly one swarm quadrant move once the decision window opens.
// Expected output: no append before the decision time, one append after, and no duplicate until commit.
func TestLeaderAppendsSwarmQuadrantMoveOnSchedule(t *testing.T) {
	leader := NewRobot("leader", nil)
	leader.raftState = raftLeader
	leader.raftTerm = 3

	leader.mu.Lock()
	leader.nextSwarmDecisionAt = time.Now().Add(2 * time.Second)
	leader.mu.Unlock()

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, raftLogTypeSwarmQuadrantMove); got != 0 {
		t.Fatalf("expected no swarm move before schedule opens, got %d", got)
	}

	leader.mu.Lock()
	leader.nextSwarmDecisionAt = time.Now().Add(-1 * time.Second)
	leader.mu.Unlock()

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, raftLogTypeSwarmQuadrantMove); got != 1 {
		t.Fatalf("expected exactly one swarm move entry after schedule opens, got %d", got)
	}

	leader.mu.Lock()
	last := leader.raftLog[len(leader.raftLog)-1]
	leader.mu.Unlock()
	if last.Type != raftLogTypeSwarmQuadrantMove {
		t.Fatalf("expected last log entry to be swarm move, got %s", last.Type)
	}

	var decision SwarmQuadrantMoveDecision
	if err := json.Unmarshal(last.Payload, &decision); err != nil {
		t.Fatalf("failed to decode swarm move payload: %v", err)
	}
	if decision.Quadrant != quadrantTopRight {
		t.Fatalf("expected first move from top-left to top-right, got %s", decision.Quadrant)
	}
	if decision.TargetX != 750.0 || decision.TargetY != 250.0 {
		t.Fatalf("expected top-right centroid target (750,250), got (%.1f,%.1f)", decision.TargetX, decision.TargetY)
	}
	if decision.Tolerance != swarmMoveTolerance {
		t.Fatalf("expected move tolerance %.1f, got %.1f", swarmMoveTolerance, decision.Tolerance)
	}

	leader.syncRaftWithPeers(context.Background())
	if got := countLogEntriesOfType(leader, raftLogTypeSwarmQuadrantMove); got != 1 {
		t.Fatalf("expected no duplicate swarm move entries before commit, got %d", got)
	}
}

// Objective: verify followers only activate quadrant movement after commit and leaders wait movement buffer + decision interval.
// Expected output: append without commit leaves movement inactive; commit activates movement and pushes next decision time out by buffer+30s.
func TestFollowerAppliesCommittedSwarmMoveOnlyAfterCommit(t *testing.T) {
	follower := NewRobot("follower", nil)
	follower.raftTerm = 2

	decision := SwarmQuadrantMoveDecision{
		Quadrant:       quadrantTopRight,
		TargetX:        750,
		TargetY:        250,
		Tolerance:      swarmMoveTolerance,
		DecisionUnixMs: time.Now().UnixMilli(),
	}
	payload, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("failed to marshal decision: %v", err)
	}

	appendResp := follower.HandleAppendEntries(&pb.AppendEntriesRequest{
		Term:         2,
		LeaderId:     "leader",
		PrevLogIndex: -1,
		PrevLogTerm:  -1,
		Entries: []*pb.RaftLogEntry{{
			Term:            2,
			Command:         payload,
			LogType:         raftLogTypeSwarmQuadrantMove,
			TimestampUnixMs: time.Now().UnixMilli(),
		}},
		LeaderCommit: -1,
	})
	if !appendResp.GetSuccess() {
		t.Fatalf("expected follower append to succeed")
	}

	follower.mu.Lock()
	inactiveMove := follower.activeSwarmMove == nil
	follower.mu.Unlock()
	if !inactiveMove {
		t.Fatalf("expected swarm move to remain inactive before commit")
	}

	before := time.Now()
	heartbeatResp := follower.HandleAppendEntries(&pb.AppendEntriesRequest{
		Term:         2,
		LeaderId:     "leader",
		PrevLogIndex: 0,
		PrevLogTerm:  2,
		Entries:      nil,
		LeaderCommit: 0,
	})
	after := time.Now()
	if !heartbeatResp.GetSuccess() {
		t.Fatalf("expected follower commit heartbeat to succeed")
	}

	follower.mu.Lock()
	active := follower.activeSwarmMove
	nextDecisionAt := follower.nextSwarmDecisionAt
	quadrant := follower.currentQuadrant
	follower.mu.Unlock()

	if active == nil {
		t.Fatalf("expected committed swarm move to activate movement")
	}
	if quadrant != quadrantTopRight {
		t.Fatalf("expected committed quadrant to become top-right, got %s", quadrant)
	}
	if active.TargetX != 750 || active.TargetY != 250 {
		t.Fatalf("expected committed target to be (750,250), got (%.1f,%.1f)", active.TargetX, active.TargetY)
	}

	expectedMin := before.Add(raftSwarmMoveBuffer + raftSwarmDecisionEvery - 200*time.Millisecond)
	expectedMax := after.Add(raftSwarmMoveBuffer + raftSwarmDecisionEvery + 200*time.Millisecond)
	if nextDecisionAt.Before(expectedMin) || nextDecisionAt.After(expectedMax) {
		t.Fatalf("expected next decision to be commit time + buffer + interval, got %v", nextDecisionAt)
	}
}

// Objective: verify requestMovement follows committed swarm target until arrival tolerance, then resumes non-swarm movement.
// Expected output: first move request targets committed swarm coordinates; once within tolerance the active directive clears.
func TestRequestMovementFollowsCommittedSwarmTargetThenResumes(t *testing.T) {
	randClient := &fakeRobotServiceClient{}
	r := newTestRobot(t, "mover", randClient)

	r.mu.Lock()
	r.X = 120
	r.Y = 120
	r.activeSwarmMove = &SwarmQuadrantMoveDecision{
		Quadrant:       quadrantTopRight,
		TargetX:        750,
		TargetY:        250,
		Tolerance:      swarmMoveTolerance,
		DecisionUnixMs: 101,
	}
	r.mu.Unlock()

	r.requestMovement(context.Background())
	if len(randClient.moveRequests) != 1 {
		t.Fatalf("expected first request to follow committed swarm target, got %d requests", len(randClient.moveRequests))
	}
	first := randClient.moveRequests[0]
	if first.TargetX != 750 || first.TargetY != 250 {
		t.Fatalf("expected committed target (750,250), got (%.1f,%.1f)", first.TargetX, first.TargetY)
	}

	r.mu.Lock()
	r.X = 745
	r.Y = 255
	r.mu.Unlock()

	r.requestMovement(context.Background())
	if len(randClient.moveRequests) != 2 {
		t.Fatalf("expected second request after arrival-handling, got %d requests", len(randClient.moveRequests))
	}
	second := randClient.moveRequests[1]
	if math.Abs(second.TargetX-750) < 0.0001 && math.Abs(second.TargetY-250) < 0.0001 {
		t.Fatalf("expected second request to resume fallback movement, still got committed target")
	}

	r.mu.Lock()
	remainingMove := r.activeSwarmMove
	r.mu.Unlock()
	if remainingMove != nil {
		t.Fatalf("expected committed swarm move to clear after entering tolerance")
	}
}
