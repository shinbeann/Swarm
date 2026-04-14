package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
)

type fakeRobotServiceClient struct {
	moveRequests  []*pb.MoveRequest
	heartbeatResp *pb.HeartbeatResponse
	networkResp   *pb.NetworkResponse
	sensorResp    *pb.SensorResponse
}

func (f *fakeRobotServiceClient) MoveToPosition(ctx context.Context, in *pb.MoveRequest, opts ...grpc.CallOption) (*pb.MoveResponse, error) {
	f.moveRequests = append(f.moveRequests, in)
	return &pb.MoveResponse{Success: true}, nil
}

func (f *fakeRobotServiceClient) GetSensorData(ctx context.Context, in *pb.SensorRequest, opts ...grpc.CallOption) (*pb.SensorResponse, error) {
	if f.sensorResp == nil {
		return &pb.SensorResponse{}, nil
	}
	return f.sensorResp, nil
}

func (f *fakeRobotServiceClient) GetNetworkData(ctx context.Context, in *pb.NetworkRequest, opts ...grpc.CallOption) (*pb.NetworkResponse, error) {
	if f.networkResp == nil {
		return &pb.NetworkResponse{}, nil
	}
	return f.networkResp, nil
}

func (f *fakeRobotServiceClient) SendHeartbeat(ctx context.Context, in *pb.HeartbeatRequest, opts ...grpc.CallOption) (*pb.HeartbeatResponse, error) {
	if f.heartbeatResp == nil {
		return &pb.HeartbeatResponse{Success: true}, nil
	}
	return f.heartbeatResp, nil
}

func newTestRobot(t *testing.T, id string, client pb.RobotServiceClient) *Robot {
	t.Helper()
	r := NewRobot(id, client)
	t.Cleanup(r.Stop)
	return r
}

func findEntry(t *testing.T, store *KnowledgeStore, id LandmarkID) *LandmarkEntry {
	t.Helper()
	for _, entry := range store.GetAll() {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("expected landmark %s to exist in store", id)
	return nil
}

func gossipRequest(sender string, timestamp int, entries ...*LandmarkEntry) *pb.PeerSyncRequest {
	msg := &GossipMessage{
		SenderID:  RobotID(sender),
		Timestamp: timestamp,
		Entries:   entries,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	return &pb.PeerSyncRequest{
		SenderId:     sender,
		Payload:      payload,
		LamportClock: int64(timestamp),
	}
}

// confirms that receiving peer gossip from 3 different reporters verifies the casualty on 3rd report
func TestKnowledgeStoreVerificationLifecycle(t *testing.T) {
	store := NewKnowledgeStore()
	loc := Location{X: 12.5, Y: 42.25}
	id := LandmarkID("casualty-1")

	if verified := store.Add(id, LandmarkCasualty, loc, "r1", 1); verified {
		t.Fatalf("expected first report to stay unverified")
	}

	entry := findEntry(t, store, id)
	if entry.Verified {
		t.Fatalf("expected casualty to remain unverified after first report")
	}
	if len(entry.Reporters) != 1 {
		t.Fatalf("expected 1 reporter after first report, got %d", len(entry.Reporters))
	}

	if verified := store.Add(id, LandmarkCasualty, loc, "r2", 2); verified {
		t.Fatalf("expected second distinct report to stay unverified")
	}

	entry = findEntry(t, store, id)
	if entry.Verified {
		t.Fatalf("expected casualty to remain unverified after second report")
	}
	if len(entry.Reporters) != 2 {
		t.Fatalf("expected 2 reporters after second report, got %d", len(entry.Reporters))
	}

	if verified := store.Add(id, LandmarkCasualty, loc, "r3", 3); !verified {
		t.Fatalf("expected third distinct report to verify the casualty")
	}

	entry = findEntry(t, store, id)
	if !entry.Verified {
		t.Fatalf("expected casualty to be marked verified after third report")
	}
	if len(entry.Reporters) != 3 {
		t.Fatalf("expected 3 reporters after third report, got %d", len(entry.Reporters))
	}

	if verified := store.Add(id, LandmarkCasualty, loc, "r3", 4); verified {
		t.Fatalf("expected duplicate report from same robot not to trigger a new verification transition")
	}
	entry = findEntry(t, store, id)
	if len(entry.Reporters) != 3 {
		t.Fatalf("expected duplicate report not to increase reporter count, got %d", len(entry.Reporters))
	}
}

// checks that receiving peer gossip from 3 different reporters verifies the casualty on 3rd report
func TestRobotOnPeerSyncVerifiesCasualtyOnThirdDistinctReporter(t *testing.T) {
	r := newTestRobot(t, "receiver", &fakeRobotServiceClient{})
	id := LandmarkID("casualty-2")
	loc := Location{X: 100, Y: 200}

	r.OnPeerSync(gossipRequest("peer-1", 1, &LandmarkEntry{ID: id, Type: LandmarkCasualty, Location: loc, Reporters: map[RobotID]int{RobotID("peer-1"): 1}}))
	entry := findEntry(t, r.store, id)
	if entry.Verified {
		t.Fatalf("expected casualty to remain unverified after first peer report")
	}
	if len(entry.Reporters) != 1 {
		t.Fatalf("expected 1 reporter after first peer report, got %d", len(entry.Reporters))
	}

	r.OnPeerSync(gossipRequest("peer-2", 2, &LandmarkEntry{ID: id, Type: LandmarkCasualty, Location: loc, Reporters: map[RobotID]int{RobotID("peer-2"): 2}}))
	entry = findEntry(t, r.store, id)
	if entry.Verified {
		t.Fatalf("expected casualty to remain unverified after second peer report")
	}
	if len(entry.Reporters) != 2 {
		t.Fatalf("expected 2 reporters after second peer report, got %d", len(entry.Reporters))
	}

	r.OnPeerSync(gossipRequest("peer-3", 3, &LandmarkEntry{ID: id, Type: LandmarkCasualty, Location: loc, Reporters: map[RobotID]int{RobotID("peer-3"): 3}}))
	entry = findEntry(t, r.store, id)
	if !entry.Verified {
		t.Fatalf("expected casualty to verify after third distinct peer report")
	}
	if len(entry.Reporters) != 3 {
		t.Fatalf("expected 3 reporters after third peer report, got %d", len(entry.Reporters))
	}
}

func TestRobotOnPeerSyncPreservesEyewitnessProvenanceAcrossRelay(t *testing.T) {
	r := newTestRobot(t, "receiver", &fakeRobotServiceClient{})
	id := LandmarkID("casualty-relay")
	loc := Location{X: 140, Y: 220}

	r.OnPeerSync(gossipRequest("peer-1", 1, &LandmarkEntry{ID: id, Type: LandmarkCasualty, Location: loc, Reporters: map[RobotID]int{RobotID("peer-1"): 1}}))
	r.OnPeerSync(gossipRequest("peer-2", 2, &LandmarkEntry{ID: id, Type: LandmarkCasualty, Location: loc, Reporters: map[RobotID]int{RobotID("peer-1"): 1}}))

	entry := findEntry(t, r.store, id)
	if _, ok := entry.Reporters[RobotID("peer-2")]; ok {
		t.Fatalf("expected relay peer-2 not to be counted as an eyewitness reporter")
	}
	if len(entry.Reporters) != 1 {
		t.Fatalf("expected relay to preserve only the original eyewitness reporter, got %d", len(entry.Reporters))
	}
}

func TestRobotTickRecordsFirstLandmarkSighting(t *testing.T) {
	client := &fakeRobotServiceClient{
		heartbeatResp: &pb.HeartbeatResponse{Success: true, X: 150, Y: 160, Heading: 0.5},
		networkResp:   &pb.NetworkResponse{},
		sensorResp: &pb.SensorResponse{
			Objects: []*pb.ObjectData{{
				Id:   "landmark-1",
				X:    158,
				Y:    160,
				Type: "landmark:casualty",
			}},
		},
	}
	r := newTestRobot(t, "scout", client)
	r.X = 150
	r.Y = 160
	r.Heading = 0.5

	r.tick(context.Background())

	entry := findEntry(t, r.store, LandmarkID("landmark-1"))
	if entry.Type != LandmarkCasualty {
		t.Fatalf("expected first sighting to preserve landmark type, got %s", entry.Type)
	}
	if entry.Location != (Location{X: 158, Y: 160}) {
		t.Fatalf("expected stored landmark location to match sensor data, got (%.1f, %.1f)", entry.Location.X, entry.Location.Y)
	}
	if entry.Verified {
		t.Fatalf("expected first sighting to stay unverified")
	}
	if _, ok := entry.Reporters[RobotID(r.ID)]; !ok {
		t.Fatalf("expected robot to record itself as the first reporter")
	}
	if len(client.moveRequests) != 0 {
		t.Fatalf("expected tick to record the sighting without moving, got %d move requests", len(client.moveRequests))
	}
}

func TestRequestMovementPrefersUnverifiedCasualtyAndStopsAfterVerification(t *testing.T) {
	rand.Seed(1)
	client := &fakeRobotServiceClient{}
	r := newTestRobot(t, "mover", client)
	r.X = 100
	r.Y = 100

	id := LandmarkID("casualty-3")
	loc := Location{X: 250, Y: 260}

	if verified := r.store.Add(id, LandmarkCasualty, loc, "peer-1", 1); verified {
		t.Fatalf("expected first report to stay unverified")
	}
	if verified := r.store.Add(id, LandmarkCasualty, loc, "peer-2", 2); verified {
		t.Fatalf("expected second report to stay unverified")
	}

	r.requestMovement(context.Background())
	if len(client.moveRequests) != 1 {
		t.Fatalf("expected one move request toward casualty, got %d", len(client.moveRequests))
	}
	firstMove := client.moveRequests[0]
	if firstMove.TargetX != loc.X || firstMove.TargetY != loc.Y {
		t.Fatalf("expected move toward casualty at (%.1f, %.1f), got (%.1f, %.1f)", loc.X, loc.Y, firstMove.TargetX, firstMove.TargetY)
	}

	if verified := r.store.Add(id, LandmarkCasualty, loc, "peer-3", 3); !verified {
		t.Fatalf("expected third distinct report to verify the casualty")
	}
	if len(r.store.UnverifiedCasualties(RobotID(r.ID))) != 0 {
		t.Fatalf("expected verified casualty to disappear from unverified casualty list")
	}

	r.requestMovement(context.Background())
	if len(client.moveRequests) != 2 {
		t.Fatalf("expected a second move request after verification, got %d", len(client.moveRequests))
	}
	secondMove := client.moveRequests[1]
	if secondMove.TargetX == loc.X && secondMove.TargetY == loc.Y {
		t.Fatalf("expected verified casualty not to be targeted again")
	}
}

func TestGossipEngineDeltaSkipsPeersWithoutNewEntries(t *testing.T) {
	robot := &Robot{
		ID:                "scout",
		Clock:             NewLamportClock(),
		store:             NewKnowledgeStore(),
		networkConditions: make(map[RobotID]*pb.NetworkData),
		routingTable:      NewRoutingTable(RobotID("scout")),
	}

	robot.networkConditions[RobotID("peer-a")] = &pb.NetworkData{Bandwidth: 10}
	robot.networkConditions[RobotID("peer-b")] = &pb.NetworkData{Bandwidth: 10}
	robot.networkConditions[RobotID("peer-c")] = &pb.NetworkData{Bandwidth: 10}

	type sent struct {
		target RobotID
		msg    *GossipMessage
	}
	var sentMsgs []sent
	engine := NewGossipEngine(robot, func(to RobotID, msg *GossipMessage) error {
		sentMsgs = append(sentMsgs, sent{target: to, msg: msg})
		return nil
	})

	engine.RecordDiscovery("casualty-a", LandmarkCasualty, Location{X: 10, Y: 10})
	engine.gossipOnce() // first delta should reach all peers

	if len(sentMsgs) != 3 {
		t.Fatalf("expected 3 sends on first gossip round, got %d", len(sentMsgs))
	}
	for _, out := range sentMsgs {
		if len(out.msg.Entries) != 1 {
			t.Fatalf("expected first gossip to send one entry, got %d", len(out.msg.Entries))
		}
		if out.msg.Entries[0].ID != "casualty-a" {
			t.Fatalf("expected first gossip entry to be casualty-a, got %s", out.msg.Entries[0].ID)
		}
	}

	engine.gossipOnce() // no new entries: should send nothing
	if len(sentMsgs) != 3 {
		t.Fatalf("expected no additional sends when there is no new delta, got %d total sends", len(sentMsgs))
	}

	engine.RecordDiscovery("casualty-b", LandmarkCasualty, Location{X: 20, Y: 20})
	engine.gossipOnce() // second delta should reach all peers

	if len(sentMsgs) != 6 {
		t.Fatalf("expected 6 total sends after second discovery, got %d", len(sentMsgs))
	}

	for _, out := range sentMsgs[3:] {
		if len(out.msg.Entries) != 1 {
			t.Fatalf("expected second gossip delta to contain one entry, got %d", len(out.msg.Entries))
		}
		if out.msg.Entries[0].ID != "casualty-b" {
			t.Fatalf("expected second gossip entry to be casualty-b, got %s", out.msg.Entries[0].ID)
		}
	}
}

func TestGossipEngineDeltaPrioritizesCasualtiesWithinBandwidthCap(t *testing.T) {
	robot := &Robot{
		ID:                "scout",
		Clock:             NewLamportClock(),
		store:             NewKnowledgeStore(),
		networkConditions: make(map[RobotID]*pb.NetworkData),
		routingTable:      NewRoutingTable(RobotID("scout")),
	}

	robot.networkConditions[RobotID("peer-a")] = &pb.NetworkData{Bandwidth: 1}

	var sentMsg *GossipMessage
	engine := NewGossipEngine(robot, func(to RobotID, msg *GossipMessage) error {
		sentMsg = msg
		return nil
	})

	engine.RecordDiscovery("obstacle-a", LandmarkObstacle, Location{X: 4, Y: 4})
	engine.RecordDiscovery("corridor-a", LandmarkCorridor, Location{X: 5, Y: 5})
	engine.RecordDiscovery("casualty-a", LandmarkCasualty, Location{X: 6, Y: 6})
	engine.gossipOnce()

	if sentMsg == nil {
		t.Fatalf("expected one gossip message to be sent")
	}
	if len(sentMsg.Entries) != 1 {
		t.Fatalf("expected bandwidth cap of 1 entry, got %d", len(sentMsg.Entries))
	}
	if sentMsg.Entries[0].Type != LandmarkCasualty {
		t.Fatalf("expected casualty to be prioritized under bandwidth cap, got %s", sentMsg.Entries[0].Type)
	}
	if sentMsg.Entries[0].ID != "casualty-a" {
		t.Fatalf("expected prioritized casualty entry casualty-a, got %s", sentMsg.Entries[0].ID)
	}

	// With no new delta after watermark update, no further send should occur.
	sentMsg = nil
	engine.gossipOnce()
	if sentMsg != nil {
		t.Fatalf("expected no gossip send when there are no new entries for peer-a")
	}
}

func TestGossipEngineOnReceiveMakesRobotIndependentDeltaSource(t *testing.T) {
	robot := &Robot{
		ID:                "robot-2",
		Clock:             NewLamportClock(),
		store:             NewKnowledgeStore(),
		networkConditions: make(map[RobotID]*pb.NetworkData),
		routingTable:      NewRoutingTable(RobotID("robot-2")),
	}
	robot.networkConditions[RobotID("robot-3")] = &pb.NetworkData{Bandwidth: 10}

	var sentMsgs []*GossipMessage
	engine := NewGossipEngine(robot, func(to RobotID, msg *GossipMessage) error {
		if to != "robot-3" {
			t.Fatalf("expected gossip target robot-3, got %s", to)
		}
		sentMsgs = append(sentMsgs, msg)
		return nil
	})

	// Simulate robot-2 receiving a discovery from robot-1 and merging it locally.
	engine.OnReceive(&GossipMessage{
		SenderID:  "robot-1",
		Timestamp: 14,
		Entries: []*LandmarkEntry{
			{
				ID:       "casualty-a",
				Type:     LandmarkCasualty,
				Location: Location{X: 100, Y: 100},
				Reporters: map[RobotID]int{
					"robot-1": 14,
				},
			},
		},
	})

	// Next gossip round after merge: robot-2 should propagate to robot-3.
	engine.gossipOnce()

	if len(sentMsgs) == 0 {
		t.Fatalf("expected robot-2 to gossip merged entry to robot-3")
	}
	firstSend := sentMsgs[0]
	if len(firstSend.Entries) != 1 {
		t.Fatalf("expected one propagated entry, got %d", len(firstSend.Entries))
	}
	if firstSend.Entries[0].ID != "casualty-a" {
		t.Fatalf("expected propagated entry casualty-a, got %s", firstSend.Entries[0].ID)
	}

	// Following round should skip because watermark has advanced.
	engine.gossipOnce()
	if len(sentMsgs) != 1 {
		t.Fatalf("expected no second send without new entries, got %d sends", len(sentMsgs))
	}
}
