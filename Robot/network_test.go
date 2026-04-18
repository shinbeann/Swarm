package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

// Helper to calculate expected network delay based on logic in applyNetworkConstraints
func expectedDelayMs(latencyMs, bandwidthMbps, payloadMb float64) float64 {
	effectiveBW := bandwidthMbps
	if effectiveBW < 0.1 {
		effectiveBW = 0.1
	}
	transferTimeSec := (payloadMb * 8.0) / effectiveBW
	return latencyMs + transferTimeSec*1000.0
}

// Objective: verify network constraints add the expected multi-hop delay.
// Expected output: the simulated network call succeeds and takes at least the calculated latency.
func TestMultiHopPacketLatency(t *testing.T) {
	r := NewRobot("r1", nil)

	latency := 50.0       // ms
	bandwidth := 10.0     // Mbps
	payloadMb := 0.002    // mb (mesh payload)

	start := time.Now()
	// applyNetworkConstraints sleeps to simulate network conditions
	success := r.applyNetworkConstraints("r2", latency, bandwidth, 1.0, payloadMb, "Mesh")
	duration := time.Since(start)

	if !success {
		t.Fatalf("expected network constraint application to succeed (100%% reliability)")
	}

	expectedMs := expectedDelayMs(latency, bandwidth, payloadMb)
	expectedDuration := time.Duration(expectedMs) * time.Millisecond

	// Allow a small margin of error for system timer precision (e.g., 5ms)
	if duration < expectedDuration-5*time.Millisecond {
		t.Fatalf("expected delay at least %v, got %v", expectedDuration, duration)
	}
}

// Objective: verify routing converges across advertisements and updates correctly after topology changes.
// Expected output: r1 learns a 3-hop route, then shortens it to 1 hop, and the expired route is pruned.
func TestRoutingConvergenceAndTopologyChanges(t *testing.T) {
	r1, r2, r3, r4 := NewRobot("r1", nil), NewRobot("r2", nil), NewRobot("r3", nil), NewRobot("r4", nil)

	// Build linear topology r1 <-> r2 <-> r3 <-> r4
	r4.routingTable.RecordDirectNeighbour("r3") // r4 knows r3
	r3.routingTable.MergeAdvertisement(RobotID(r4.ID), r4.routingTable.BuildAdvertisement()) // r3 learns r4
	r3.routingTable.RecordDirectNeighbour("r2") // r3 knows r2
	r2.routingTable.MergeAdvertisement(RobotID(r3.ID), r3.routingTable.BuildAdvertisement()) // r2 learns r4(2 hops) and r3(1 hop)
	r2.routingTable.RecordDirectNeighbour("r1") // r2 knows r1
	r1.routingTable.MergeAdvertisement(RobotID(r2.ID), r2.routingTable.BuildAdvertisement()) // r1 learns r4(3 hops)

	route, ok := r1.routingTable.GetRoute("r4")
	if !ok || route.HopCount != 3 {
		t.Fatalf("expected r1 to reach r4 in 3 hops, got ok=%v, hops=%d", ok, route.HopCount)
	}

	// Topology change: r1 moves next to r4
	r1.routingTable.RecordDirectNeighbour("r4")
	route, ok = r1.routingTable.GetRoute("r4")
	if !ok || route.HopCount != 1 {
		t.Fatalf("expected r1 to reach r4 in 1 hop after moving, got hops=%d", route.HopCount)
	}

	// Topology change: r4 goes offline (simulate pruning)
	// We advance time for pruning by rewriting LastUpdated.
	r1.routingTable.mu.Lock()
	r1.routingTable.routes["r4"].LastUpdated = time.Now().Add(-15 * time.Second)
	r1.routingTable.mu.Unlock()

	r1.routingTable.PruneExpired(10 * time.Second)
	_, ok = r1.routingTable.GetRoute("r4")
	if ok {
		t.Fatalf("expected route to r4 to be pruned")
	}
}

// Objective: verify a minority partition cannot elect a leader and the healed leader steps down on a higher term.
// Expected output: the minority stays leaderless and r1 demotes to follower after the partition heals.
func TestNetworkPartitionsAndRaftSplitBrain(t *testing.T) {
	robots := setupCluster(5)
	r1, r2, r3, r4, r5 := robots[0], robots[1], robots[2], robots[3], robots[4]

	// 1. Initial State: r1 is elected leader securely
	electLeader(t, r1, r2, r3, r4, r5)

	// 2. Partition: P_Maj (r1, r2, r3) and P_Min (r4, r5)
	// Maj replicates 1 new log
	manuallyAppendLog(r1, r1.raftTerm, "cmd", []byte("maj_log"))
	replicateLog(r1, r2)
	replicateLog(r1, r3)
	r1.mu.Lock()
	r1.advanceCommitIndexLocked()
	r1.mu.Unlock()

	// Min partition cannot elect leader
	forceElectionTimeout(r4)
	r4.maybeStartElection() // r4 becomes candidate term 2 (if it was term 1)
	req := r4.buildVoteRequest()
	resp := r5.HandleRequestVote(req)
	r4.handleVoteResponse(r5.ID, resp)

	r4.mu.Lock()
	state := r4.raftState
	r4.mu.Unlock()
	if state == raftLeader {
		t.Fatalf("minority partition should not be able to elect a leader")
	}

	// 3. Heal Partition: r1 sends heartbeat to r4 and r5
	// Because we don't use Pre-Vote, r4 has incremented its term during the partition.
	// When r1 sends a heartbeat, r4 will reject it with a higher term, causing r1 to step down.
	reqHeartbeat := r1.buildAppendEntriesRequestForPeer(r4.ID)
	respHeartbeat := r4.HandleAppendEntries(reqHeartbeat)
	
	if respHeartbeat.GetSuccess() {
		t.Fatalf("expected r4 to reject r1's heartbeat due to higher term")
	}

	r1.handleAppendResponse(r4.ID, reqHeartbeat, respHeartbeat)

	r1.mu.Lock()
	r1State := r1.raftState
	r1.mu.Unlock()

	if r1State != raftFollower {
		t.Fatalf("r1 should step down to follower after seeing r4's higher term")
	}
}

// Objective: verify routed messages are dropped when TTL expires.
// Expected output: the router reports the message as undelivered.
func TestMeshTTLDrops(t *testing.T) {
	r1 := NewRobot("r1", nil)

	// Setup a mesh router
	mr := NewMeshRouter(r1)

	// HandleRouteMessage with TTL = 1
	req := &pb.RoutedMessageRequest{
		DestinationId: "r2",
		Ttl:           1,
	}

	resp := mr.HandleRouteMessage(context.Background(), req)
	if resp.GetDelivered() {
		t.Fatalf("expected message to be dropped due to TTL exhaustion")
	}
}
