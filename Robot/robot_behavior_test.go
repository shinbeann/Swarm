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

func TestGossipEngineCyclesThroughPeers(t *testing.T) {
	robot := &Robot{
		ID:                "scout",
		Clock:             NewLamportClock(),
		store:             NewKnowledgeStore(),
		networkConditions: make(map[RobotID]*pb.NetworkData),
		routingTable:      NewRoutingTable(RobotID("scout")),
	}

	robot.networkConditions[RobotID("peer-c")] = &pb.NetworkData{}
	robot.networkConditions[RobotID("peer-a")] = &pb.NetworkData{}
	robot.networkConditions[RobotID("peer-b")] = &pb.NetworkData{}

	var targets []RobotID
	engine := NewGossipEngine(robot, func(to RobotID, msg *GossipMessage) error {
		targets = append(targets, to)
		return nil
	})

	for i := 0; i < 6; i++ {
		engine.gossipOnce()
	}

	expected := []RobotID{"peer-a", "peer-b", "peer-c", "peer-a", "peer-b", "peer-c"}
	if len(targets) != len(expected) {
		t.Fatalf("expected %d gossip sends, got %d", len(expected), len(targets))
	}
	for i, target := range expected {
		if targets[i] != target {
			t.Fatalf("expected gossip target %d to be %s, got %s", i, target, targets[i])
		}
	}
}
