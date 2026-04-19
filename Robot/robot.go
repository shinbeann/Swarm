package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type raftNodeState int

const (
	raftFollower raftNodeState = iota
	raftCandidate
	raftLeader
)

const (
	raftLeaderPingInterval = 10 * time.Second
	raftElectionMinTimeout = 10 * time.Second // Increased for multi-hop mesh latency.
	raftElectionJitter     = 7 * time.Second  // Increased for multi-hop mesh latency.
	raftSyncInterval       = 500 * time.Millisecond
	raftCandidateVoteRetry = 2 * time.Second
)

type RaftLogEntry struct {
	Term            int64
	Index           int64
	Type            string
	Payload         []byte
	TimestampUnixMs int64
}

type Robot struct {
	mu sync.Mutex

	ID      string
	X       float64
	Y       float64
	Heading float64

	Client       pb.RobotServiceClient
	RaftObserver pb.RaftObserverServiceClient
	Clock        *LamportClock
	lastSync     time.Time
	paused       bool

	store               *KnowledgeStore
	networkMu           sync.RWMutex
	networkConditions   map[RobotID]*pb.NetworkData
	gossip              *GossipEngine
	discoveredLandmarks map[LandmarkID]bool

	// Last sensed obstacle angle (radians from robot); only valid when nearObstacle is true
	nearObstacle  bool
	obstacleAngle float64

	// Mesh routing layer.
	routingTable *RoutingTable
	meshRouter   *MeshRouter

	// Raft-like coordination state.
	raftState          raftNodeState
	raftTerm           int64
	knownLeaderID      string
	votedFor           string
	raftLog            []RaftLogEntry
	commitIndex        int64
	lastAppliedIndex   int64
	lastRaftPingSentAt time.Time
	lastVoteRequestAt  time.Time
	lastLeaderSeenAt   time.Time
	lastElectionAt     time.Time
	electionTimeout    time.Duration
	votesGranted       map[string]bool
	knownPeerIDs       map[string]struct{}
	nextIndex          map[string]int64
	matchIndex         map[string]int64
	totalNodes         int
}

func NewRobot(id string, client pb.RobotServiceClient) *Robot {
	r := &Robot{
		ID:                  id,
		X:                   100 + rand.Float64()*200,
		Y:                   100 + rand.Float64()*200,
		Heading:             rand.Float64() * 2 * math.Pi,
		Client:              client,
		Clock:               NewLamportClock(),
		lastSync:            time.Now(),
		store:               NewKnowledgeStore(),
		networkConditions:   make(map[RobotID]*pb.NetworkData),
		discoveredLandmarks: make(map[LandmarkID]bool),
		raftState:           raftFollower,
		raftTerm:            1,
		lastLeaderSeenAt:    time.Now(),
		raftLog:             make([]RaftLogEntry, 0),
		commitIndex:         -1,
		lastAppliedIndex:    -1,
		electionTimeout:     randomElectionTimeout(),
		votesGranted:        make(map[string]bool),
		knownPeerIDs:        make(map[string]struct{}),
		nextIndex:           make(map[string]int64),
		matchIndex:          make(map[string]int64),
		totalNodes:          1,
	}

	r.routingTable = NewRoutingTable(RobotID(id))
	r.meshRouter = NewMeshRouter(r)

	r.gossip = NewGossipEngine(r, r.sendGossipMessage)
	r.gossip.Start()
	return r
}

func (r *Robot) SetRaftObserverClient(client pb.RaftObserverServiceClient) {
	r.mu.Lock()
	r.RaftObserver = client
	r.mu.Unlock()
}

func (r *Robot) Stop() {
	if r.gossip != nil {
		r.gossip.Stop()
	}
}

func (r *Robot) Run(ctx context.Context) {
	heartbeatTicker := time.NewTicker(30 * time.Millisecond)
	moveTicker := time.NewTicker(2 * time.Second)
	raftTicker := time.NewTicker(raftSyncInterval)
	defer heartbeatTicker.Stop()
	defer moveTicker.Stop()
	defer raftTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			r.tick(ctx)
		case <-moveTicker.C:
			if r.isPaused() {
				continue
			}
			r.requestMovement(ctx)
		case <-raftTicker.C:
			if r.isPaused() {
				continue
			}
			r.tickRaft(ctx)
		}
	}
}

func (r *Robot) lastRaftLogIndexLocked() int64 {
	if len(r.raftLog) == 0 {
		return -1
	}
	return r.raftLog[len(r.raftLog)-1].Index
}

func (r *Robot) tick(ctx context.Context) {
	r.Clock.Tick()

	r.mu.Lock()
	knownLeaderID := r.knownLeaderID
	currentX := r.X
	currentY := r.Y
	currentHeading := r.Heading
	currentTerm := r.raftTerm
	lastLogIndex := r.lastRaftLogIndexLocked()
	commitIndex := r.commitIndex
	r.mu.Unlock()

	// Heartbeat — WorldEngine returns the canonical position
	heartbeatResp, err := r.Client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		RobotId:       r.ID,
		X:             currentX,
		Y:             currentY,
		Heading:       currentHeading,
		KnownLeaderId: knownLeaderID,
		CurrentTerm:   currentTerm,
		LastLogIndex:  lastLogIndex,
		CommitIndex:   commitIndex,
	})
	if err != nil {
		log.Printf("worldheartbeat error: %v", err)
	} else if heartbeatResp != nil && heartbeatResp.GetSuccess() {
		// Sync local position mirror from the authoritative WorldEngine state
		r.mu.Lock()
		r.X = heartbeatResp.GetX()
		r.Y = heartbeatResp.GetY()
		r.Heading = heartbeatResp.GetHeading()
		r.mu.Unlock()
	} else if heartbeatResp != nil && !heartbeatResp.GetSuccess() {
		log.Printf("world rejected heartbeat for %s — shutting down (removed from simulation)", r.ID)
		r.Stop()
		os.Exit(0)
	}

	r.refreshNetworkConditions(ctx) // getting new local network data regarding one-hop peers from world engine
	if r.isPaused() {
		return
	}

	// Sensor — detect nearby obstacles; result is used by requestMovement
	sensorResp, err := r.Client.GetSensorData(ctx, &pb.SensorRequest{RobotId: r.ID})
	if err != nil {
		return
	}

	r.mu.Lock()
	currentX = r.X
	currentY = r.Y
	r.nearObstacle = false
	r.mu.Unlock()

	for _, obj := range sensorResp.GetObjects() {

		if obj.GetType() == "obstacle" {
			distX := obj.GetX() - currentX
			distY := obj.GetY() - currentY
			dist := math.Sqrt(distX*distX + distY*distY)
			if dist <= 5.0 {
				r.mu.Lock()
				r.nearObstacle = true
				r.obstacleAngle = math.Atan2(distY, distX)
				r.mu.Unlock()
				break
			}
		}

		if strings.HasPrefix(obj.GetType(), "landmark:") {
			log.Printf("[Robot] Near landmark %s of type %s at (%.1f, %.1f)", obj.GetId(), obj.GetType(), obj.GetX(), obj.GetY())

			id := LandmarkID(obj.GetId())
			r.mu.Lock()
			alreadyDiscovered := r.discoveredLandmarks[id]
			if !alreadyDiscovered {
				r.discoveredLandmarks[id] = true
			}
			r.mu.Unlock()
			if !alreadyDiscovered {
				ltype := LandmarkType(strings.TrimPrefix(obj.GetType(), "landmark:"))
				log.Printf("[Robot] Discovered landmark %s of type %s at (%.1f, %.1f)", id, ltype, obj.GetX(), obj.GetY())
				r.gossip.RecordDiscovery(id, ltype, Location{X: obj.GetX(), Y: obj.GetY()})
			}
		}
	}
}

func (r *Robot) refreshNetworkConditions(ctx context.Context) {
	networkResp, err := r.Client.GetNetworkData(ctx, &pb.NetworkRequest{RobotId: r.ID})
	if err != nil {
		log.Printf("world network data error: %v", err)
		r.networkMu.Lock()
		r.networkConditions = make(map[RobotID]*pb.NetworkData)
		r.networkMu.Unlock()
		return
	}

	conditions := networkResp.GetNetworkConditions()
	r.networkMu.Lock()
	r.networkConditions = make(map[RobotID]*pb.NetworkData, len(conditions))
	for _, cond := range conditions {
		targetID := RobotID(cond.GetTargetRobotId())
		r.networkConditions[targetID] = cond
	}
	r.networkMu.Unlock()
	r.setPaused(networkResp.GetSimulationPaused())
}

func (r *Robot) activeNeighbours() []RobotID {
	r.networkMu.RLock()
	defer r.networkMu.RUnlock()

	active := make([]RobotID, 0, len(r.networkConditions))
	for id := range r.networkConditions {
		active = append(active, id)
	}
	return active
}

func (r *Robot) networkCondition(id RobotID) (*pb.NetworkData, bool) {
	r.networkMu.RLock()
	defer r.networkMu.RUnlock()
	cond, ok := r.networkConditions[id]
	return cond, ok
}

func (r *Robot) tickRaft(ctx context.Context) {
	if r.isPaused() {
		return
	}
	r.maybeStartElection()
	r.syncRaftWithPeers(ctx)

	r.mu.Lock()
	r.applyCommittedEntriesLocked()
	r.mu.Unlock()

	r.publishRaftSnapshot(ctx)
}

//helper function for publishRaftSnapshot
func logMessageForSnapshot(entry RaftLogEntry) string {
	switch entry.Type {
	case "casualty_verified":
		var casualty LandmarkEntry
		if err := json.Unmarshal(entry.Payload, &casualty); err == nil {
			return string(casualty.ID)
		}
		return string(entry.Payload)
	case "leader_ping":
		return string(entry.Payload)
	default:
		return string(entry.Payload)
	}
}

// Sends the latest raft log entries to the RaftObserver (WorldEngine).
// Only the leader sends snapshots
// Called during each raft tick after applying committed entries
func (r *Robot) publishRaftSnapshot(ctx context.Context) {
	r.mu.Lock()
	if r.raftState != raftLeader || r.RaftObserver == nil {
		r.mu.Unlock()
		return
	}

	const maxEntries = 120
	start := 0
	if len(r.raftLog) > maxEntries {
		start = len(r.raftLog) - maxEntries
	}

	//RaftLogSnapshotEntry is defined by the proto file
	entries := make([]*pb.RaftLogSnapshotEntry, 0, len(r.raftLog)-start)
	for i := start; i < len(r.raftLog); i++ {
		entry := r.raftLog[i]
		entries = append(entries, &pb.RaftLogSnapshotEntry{
			Term:            entry.Term,
			Index:           entry.Index,
			LogType:         entry.Type,
			Message:         logMessageForSnapshot(entry),
			TimestampUnixMs: entry.TimestampUnixMs,
		})
	}

	// RaftLogSnapshotRequest is defined by the proto file; contains metadata about the snapshot and the log entries themselves
	request := &pb.RaftLogSnapshotRequest{
		LeaderId:    r.ID,
		CurrentTerm: r.raftTerm,
		CommitIndex: r.commitIndex,
		Entries:     entries,
	}
	observer := r.RaftObserver
	r.mu.Unlock()

	if _, err := observer.PublishRaftLogSnapshot(ctx, request); err != nil {
		log.Printf("[Raft] %s failed to publish raft snapshot: %v", r.ID, err)
	}
}

// requestMovement picks a target position and asks the WorldEngine to move the robot there.
// The WorldEngine has full authority over the actual path and final position.
func (r *Robot) requestMovement(ctx context.Context) {
	if r.isPaused() {
		return
	}

	r.mu.Lock()
	currentX := r.X
	currentY := r.Y
	nearObstacle := r.nearObstacle
	obstacleAngle := r.obstacleAngle
	r.mu.Unlock()

	// Priority 1: move toward closest unverified casualty
	unverified := r.store.UnverifiedCasualties(RobotID(r.ID))
	if len(unverified) > 0 {
		target := r.closestCasualty(unverified)
		if target != nil {
			desiredHeading := math.Atan2(
				target.Location.Y-currentY,
				target.Location.X-currentX,
			)
			_, err := r.Client.MoveToPosition(ctx, &pb.MoveRequest{
				RobotId:        r.ID,
				TargetX:        target.Location.X,
				TargetY:        target.Location.Y,
				DesiredHeading: desiredHeading,
			})
			if err != nil {
				log.Printf("[move] %s failed to move toward casualty %s: %v",
					r.ID, target.ID, err)
			} else {
				log.Printf("[move] %s diverting to verify casualty %s at (%.1f,%.1f) reporters:%d",
					r.ID, target.ID, target.Location.X, target.Location.Y, len(target.Reporters))
			}
			return // skip random movement this tick
		}
	}

	// Priority 2+3: obstacle avoidance then random
	distance := 100.0 + rand.Float64()*200.0
	angle := rand.Float64() * 2 * math.Pi

	if nearObstacle {
		// Aim away from the detected obstacle with a small random spread
		angle = obstacleAngle + math.Pi + (rand.Float64()*0.5 - 0.25)
	}

	targetX := currentX + math.Cos(angle)*distance
	targetY := currentY + math.Sin(angle)*distance

	// Keep well clear of boundary walls
	targetX = math.Max(50, math.Min(950, targetX))
	targetY = math.Max(50, math.Min(950, targetY))

	desiredHeading := math.Atan2(targetY-currentY, targetX-currentX)

	_, err := r.Client.MoveToPosition(ctx, &pb.MoveRequest{
		RobotId:        r.ID,
		TargetX:        targetX,
		TargetY:        targetY,
		DesiredHeading: desiredHeading,
	})
	if err != nil {
		log.Printf("[move] %s random movement error: %v", r.ID, err)
	}
}

// closestCasualty returns the entry from candidates that is nearest to this
// robot's current position. Returns nil only if candidates is empty.
func (r *Robot) closestCasualty(candidates []*LandmarkEntry) *LandmarkEntry {
	if len(candidates) == 0 {
		return nil
	}
	var closest *LandmarkEntry
	minDist := math.MaxFloat64
	for _, e := range candidates {
		dx := e.Location.X - r.X
		dy := e.Location.Y - r.Y
		dist := math.Sqrt(dx*dx + dy*dy)
		if dist < minDist {
			minDist = dist
			closest = e
		}
	}
	return closest
}

func (r *Robot) sendGossipMessage(to RobotID, msg *GossipMessage) error {
	if r.isPaused() {
		return fmt.Errorf("robot paused")
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal gossip payload: %w", err)
	}

	cond, ok := r.networkCondition(to)
	if !ok {
		return fmt.Errorf("no current network condition for %s", to)
	}

	payloadMB := float64(len(payload)) / 1000000.0
	if !r.applyNetworkConstraints(string(to), cond.GetLatency(), cond.GetBandwidth(), cond.GetReliability(), payloadMB, "P2P") {
		return fmt.Errorf("gossip packet dropped to %s", to)
	}

	peerAddr := string(to) + ":50052"
	conn, err := grpc.NewClient(peerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial peer %s: %w", to, err)
	}
	defer conn.Close()

	peerClient := pb.NewPeerServiceClient(conn)
	syncCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := peerClient.SyncData(syncCtx, &pb.PeerSyncRequest{
		SenderId:     r.ID,
		Payload:      payload,
		LamportClock: int64(r.Clock.Time()),
	})
	if resp != nil && resp.GetReceived() {
		log.Printf("[Gossip Send] %s successfully sent gossip to %s", r.ID, to)
	} else if err != nil {
		log.Printf("[Gossip Send] %s failed to send gossip to %s: %v", r.ID, to, err)
		return fmt.Errorf("send gossip to %s: %w", to, err)
	}
	return nil
}

func (r *Robot) OnPeerSync(req *pb.PeerSyncRequest) {
	if r.isPaused() {
		return
	}

	r.Clock.Update(int(req.GetLamportClock()))
	log.Printf("[Gossip Recv] Robot %s received payload from %s", r.ID, req.GetSenderId())

	var msg GossipMessage
	if err := json.Unmarshal(req.GetPayload(), &msg); err == nil {
		// payload := req.GetPayload()
		// if len(payload) > 0 {
		// 	var pretty interface{}
		// 	if json.Unmarshal(payload, &pretty) == nil {
		// 		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
		// 			log.Printf("[Gossip Recv] Payload from %s:\n%s", req.GetSenderId(), string(b))
		// 		} else {
		// 			log.Printf("[Gossip Recv] Payload from %s (raw): %s", req.GetSenderId(), string(payload))
		// 		}
		// 	} else {
		// 		log.Printf("[Gossip Recv] Payload from %s (raw hex): %x", req.GetSenderId(), payload)
		// 	}
		// }
		r.gossip.OnReceive(&msg)
		return
	}

}

func (r *Robot) syncRaftWithPeers(ctx context.Context) {
	if r.isPaused() {
		return
	}

	// Remove stale multi-hop routes before deriving Raft topology.
	r.routingTable.PruneExpired(routeExpiryTimeout)

	// Refresh direct-neighbour topology from current network conditions.
	peerIDs := r.activeNeighbours()
	for _, peerID := range peerIDs {
		// Record direct neighbours in the routing table.
		r.routingTable.RecordDirectNeighbour(peerID)
	}
	currentPeers := r.updateRaftPeerTopology()

	r.mu.Lock()
	now := time.Now()
	state := r.raftState
	if state == raftLeader {
		r.appendVerifiedCasualtiesLocked(now)
	}
	sendCandidateVotes := false
	if state == raftCandidate {
		sendCandidateVotes = r.shouldSendCandidateVotesLocked(now)
	}
	r.mu.Unlock()

	if state == raftFollower {
		return
	}
	if state == raftCandidate && !sendCandidateVotes {
		return
	}

	// Iterate the secure array of peers returned directly by updateRaftPeerTopology
	for _, targetID := range currentPeers {
		go func(tID string) {
			r.mu.Lock()
			currState := r.raftState
			r.mu.Unlock()

			switch currState {
			case raftCandidate:
				voteReq := r.buildVoteRequest()
				if voteReq == nil {
					return
				}
				payload, err := proto.Marshal(voteReq)
				if err != nil {
					return
				}
				log.Printf("[Raft Send] %s -> %s RequestVote term=%d (mesh)", r.ID, tID, voteReq.GetTerm())
				respBytes, err := r.meshRouter.SendMessage(RobotID(tID), "RequestVote", payload)
				if err != nil {
					return
				}
				var resp pb.VoteResponse
				if err := proto.Unmarshal(respBytes, &resp); err != nil {
					return
				}
				r.handleVoteResponse(tID, &resp)

			case raftLeader:
				appendReq := r.buildAppendEntriesRequestForPeer(tID)
				if appendReq == nil {
					return
				}
				payload, err := proto.Marshal(appendReq)
				if err != nil {
					return
				}
				if len(appendReq.GetEntries()) == 0 {
					log.Printf("[Raft Send] %s -> %s heartbeat term=%d (mesh)", r.ID, tID, appendReq.GetTerm())
				} else {
					log.Printf("[Raft Send] %s -> %s append term=%d entries=%d (mesh)", r.ID, tID, appendReq.GetTerm(), len(appendReq.GetEntries()))
				}
				respBytes, err := r.meshRouter.SendMessage(RobotID(tID), "AppendEntries", payload)
				if err != nil {
					return
				}
				var resp pb.AppendEntriesResponse
				if err := proto.Unmarshal(respBytes, &resp); err != nil {
					return
				}

				r.handleAppendResponse(tID, appendReq, &resp)
			}
		}(targetID)
	}
}

func (r *Robot) applyNetworkConstraints(targetID string, latencyMs, bandwidthMbps, reliability, payloadMb float64, channel string) bool {
	if rand.Float64() > reliability {
		log.Printf("[%s Send] Packet from %s to %s DROP (Reliability: %.2f)", channel, r.ID, targetID, reliability)
		return false
	}

	effectiveBW := math.Max(bandwidthMbps, 0.1)
	transferTimeSec := (payloadMb * 8.0) / effectiveBW
	transferTimeMs := transferTimeSec * 1000.0
	totalDelayMs := latencyMs + transferTimeMs
	time.Sleep(time.Duration(totalDelayMs) * time.Millisecond)
	return true
}

func (r *Robot) maybeStartElection() {
	if r.isPaused() {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if r.raftState == raftLeader {
		return
	}

	if now.Sub(r.lastLeaderSeenAt) < r.electionTimeout {
		return
	}

	if !r.lastElectionAt.IsZero() && now.Sub(r.lastElectionAt) < r.electionTimeout {
		return
	}

	r.raftState = raftCandidate
	r.raftTerm++
	r.knownLeaderID = ""
	r.votedFor = r.ID
	r.votesGranted = map[string]bool{r.ID: true}
	r.lastVoteRequestAt = time.Time{}
	r.lastElectionAt = now
	r.electionTimeout = randomElectionTimeout()

	log.Printf("[Raft] Robot %s started election for term %d", r.ID, r.raftTerm)
}

func (r *Robot) updateRaftPeerTopology() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Collect all reachable peers from the routing table (includes multi-hop).
	allReachable := r.routingTable.GetAllReachable()

	r.knownPeerIDs = make(map[string]struct{}, len(allReachable))
	currentPeers := make([]string, 0, len(allReachable))

	for id := range allReachable {
		strID := string(id)
		if strID == "" || strID == r.ID {
			continue
		}
		r.knownPeerIDs[strID] = struct{}{}
		currentPeers = append(currentPeers, strID)

		// If we haven't seen this peer before, initialize Raft tracking state for it.
		// Assume new peers start with empty logs, so nextIndex should point to the end of our log and matchIndex should be -1.
		if _, ok := r.nextIndex[strID]; !ok {
			r.nextIndex[strID] = int64(len(r.raftLog))
		}
		if _, ok := r.matchIndex[strID]; !ok {
			r.matchIndex[strID] = -1
		}
	}

	r.totalNodes = len(r.knownPeerIDs) + 1
	return currentPeers
}

func (r *Robot) buildVoteRequest() *pb.VoteRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.raftState != raftCandidate {
		return nil
	}
	lastIdx, lastTerm := r.getLastLogInfoLocked()
	return &pb.VoteRequest{
		Term:         r.raftTerm,
		CandidateId:  r.ID,
		LastLogIndex: lastIdx,
		LastLogTerm:  lastTerm,
	}
}

func (r *Robot) maybeAppendLeaderPingLocked(now time.Time) {
	if r.raftState != raftLeader {
		return
	}

	if !r.lastRaftPingSentAt.IsZero() && now.Sub(r.lastRaftPingSentAt) < raftLeaderPingInterval {
		return
	}

	timestampMs := now.UnixMilli()
	pingPayload := []byte(fmt.Sprintf("leader-ping:%s:%d", r.ID, timestampMs))
	entry := RaftLogEntry{
		Term:            r.raftTerm,
		Index:           int64(len(r.raftLog)),
		Type:            "leader_ping",
		Payload:         pingPayload,
		TimestampUnixMs: timestampMs,
	}

	r.raftLog = append(r.raftLog, entry)
	r.matchIndex[r.ID] = entry.Index
	r.lastRaftPingSentAt = now
	r.lastLeaderSeenAt = now
	log.Printf("[Raft] Leader %s appended entry for propagation: idx=%d term=%d", r.ID, entry.Index, entry.Term)
}

func (r *Robot) shouldSendCandidateVotesLocked(now time.Time) bool {
	if r.raftState != raftCandidate {
		return false
	}

	// Giving a bit of time between vote requests to allow for responses to come back and avoid spamming the network with requests when there are issues.
	if !r.lastVoteRequestAt.IsZero() && now.Sub(r.lastVoteRequestAt) < raftCandidateVoteRetry {
		return false
	}

	r.lastVoteRequestAt = now
	return true
}

// appendVerifiedCasualtiesLocked scans the store for casualty entries that have
// reached quorum and appends each one exactly once.
// Must be called with r.mu held.
func (r *Robot) appendVerifiedCasualtiesLocked(now time.Time) {
	if r.raftState != raftLeader {
		return
	}

	for _, entry := range r.store.PendingCasualtyVerifications() {
		if r.hasCasualtyVerificationLogLocked(entry.ID) {
			continue
		}

		payload, err := json.Marshal(entry)
		if err != nil {
			log.Printf("[Raft] failed to marshal casualty_verified payload: %v", err)
			continue
		}
		logEntry := RaftLogEntry{
			Term:            r.raftTerm,
			Index:           int64(len(r.raftLog)),
			Type:            "casualty_verified",
			Payload:         payload,
			TimestampUnixMs: now.UnixMilli(),
		}
		r.raftLog = append(r.raftLog, logEntry)
		r.matchIndex[r.ID] = logEntry.Index
		log.Printf("[Raft] Leader %s appended casualty_verified for %s idx=%d term=%d",
			r.ID, entry.ID, logEntry.Index, logEntry.Term)
	}
}

func (r *Robot) hasCasualtyVerificationLogLocked(casualtyID LandmarkID) bool {
	for _, entry := range r.raftLog {
		if entry.Type != "casualty_verified" {
			continue
		}

		var casualty LandmarkEntry
		if err := json.Unmarshal(entry.Payload, &casualty); err != nil {
			continue
		}
		if casualty.ID == casualtyID {
			return true
		}
	}

	return false
}

// applyCommittedEntriesLocked applies newly committed log entries exactly once.
// Must be called with r.mu held.
func (r *Robot) applyCommittedEntriesLocked() {
	for idx := r.lastAppliedIndex + 1; idx <= r.commitIndex; idx++ {
		if idx < 0 || idx >= int64(len(r.raftLog)) {
			return
		}

		entry := r.raftLog[idx]
		switch entry.Type {
		case "casualty_verified":
			var casualty LandmarkEntry
			if err := json.Unmarshal(entry.Payload, &casualty); err != nil {
				log.Printf("[Raft] failed to apply casualty_verified idx=%d: %v", idx, err)
				break
			}
			r.store.ApplyCasualtyVerified(&casualty)
			log.Printf("[Raft] %s applied committed casualty_verified idx=%d casualty=%s",
				r.ID, idx, casualty.ID)
		}

		r.lastAppliedIndex = idx
	}
}

func (r *Robot) buildAppendEntriesRequestForPeer(peerID string) *pb.AppendEntriesRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.raftState != raftLeader {
		return nil
	}

	nextIdx, ok := r.nextIndex[peerID]
	if !ok {
		nextIdx = int64(len(r.raftLog))
		r.nextIndex[peerID] = nextIdx
	}

	prevIdx := nextIdx - 1
	prevTerm := int64(-1)
	if prevIdx >= 0 && prevIdx < int64(len(r.raftLog)) {
		prevTerm = r.raftLog[prevIdx].Term
	}

	entries := make([]*pb.RaftLogEntry, 0)
	for i := nextIdx; i < int64(len(r.raftLog)); i++ {
		e := r.raftLog[i]
		entries = append(entries, &pb.RaftLogEntry{
			Term:            e.Term,
			Command:         append([]byte(nil), e.Payload...),
			LogType:         e.Type,
			TimestampUnixMs: e.TimestampUnixMs,
		})
	}

	return &pb.AppendEntriesRequest{
		Term:         r.raftTerm,
		LeaderId:     r.ID,
		PrevLogIndex: prevIdx,
		PrevLogTerm:  prevTerm,
		Entries:      entries,
		LeaderCommit: r.commitIndex,
	}
}

func (r *Robot) handleVoteResponse(peerID string, resp *pb.VoteResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if resp == nil {
		return
	}

	if resp.GetTerm() > r.raftTerm {
		// have to give up leadership attempt if it learns of a higher term
		r.becomeFollowerLocked(resp.GetTerm(), "")
		return
	}

	if r.raftState != raftCandidate {
		return
	}

	if resp.GetVoteGranted() {
		r.votesGranted[peerID] = true
		if len(r.votesGranted) > r.totalNodes/2 {
			r.becomeLeaderLocked()
		}
	}
}

func (r *Robot) handleAppendResponse(peerID string, req *pb.AppendEntriesRequest, resp *pb.AppendEntriesResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if resp == nil {
		return
	}

	if resp.GetTerm() > r.raftTerm {
		r.becomeFollowerLocked(resp.GetTerm(), "")
		return
	}

	if r.raftState != raftLeader || req.GetTerm() != r.raftTerm {
		return
	}

	if resp.GetSuccess() {
		if len(req.GetEntries()) > 0 {
			lastIdx := req.GetPrevLogIndex() + int64(len(req.GetEntries()))
			r.matchIndex[peerID] = lastIdx
			r.nextIndex[peerID] = lastIdx + 1
			r.advanceCommitIndexLocked()
		}
		return
	}

	if r.nextIndex[peerID] > 0 {
		r.nextIndex[peerID]--
	}
}

func (r *Robot) becomeLeaderLocked() {
	r.raftState = raftLeader
	r.knownLeaderID = r.ID
	r.votedFor = ""
	r.lastVoteRequestAt = time.Time{}
	r.lastLeaderSeenAt = time.Now()
	r.lastRaftPingSentAt = time.Time{}

	lastLogIndex := int64(len(r.raftLog))
	for peerID := range r.knownPeerIDs {
		r.nextIndex[peerID] = lastLogIndex
		r.matchIndex[peerID] = -1
	}
	r.matchIndex[r.ID] = lastLogIndex - 1

	log.Printf("[Raft] Robot %s became leader for term %d", r.ID, r.raftTerm)
}

func (r *Robot) advanceCommitIndexLocked() {
	for idx := r.commitIndex + 1; idx < int64(len(r.raftLog)); idx++ {
		if r.raftLog[idx].Term != r.raftTerm {
			continue
		}

		replicated := 1
		for peerID := range r.knownPeerIDs {
			if r.matchIndex[peerID] >= idx {
				replicated++
			}
		}

		if replicated > r.totalNodes/2 {
			r.commitIndex = idx
			log.Printf("[Raft] Leader %s term=%d achieved quorum for log idx=%d, committing.", r.ID, r.raftTerm, idx)
		}
	}
}

func (r *Robot) HandleRequestVote(req *pb.VoteRequest) *pb.VoteResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return &pb.VoteResponse{Term: r.raftTerm, VoteGranted: false}
	}

	log.Printf("[Raft Receive] %s <- %s RequestVote term=%d", r.ID, req.GetCandidateId(), req.GetTerm())

	resp := &pb.VoteResponse{Term: r.raftTerm, VoteGranted: false}
	if req.GetTerm() < r.raftTerm {
		return resp
	}

	if req.GetTerm() > r.raftTerm {
		r.becomeFollowerLocked(req.GetTerm(), "")
	}

	voteGranted := r.shouldGrantVoteLocked(req.GetCandidateId(), req.GetLastLogIndex(), req.GetLastLogTerm())
	resp.Term = r.raftTerm
	resp.VoteGranted = voteGranted
	return resp
}

func (r *Robot) HandleAppendEntries(req *pb.AppendEntriesRequest) *pb.AppendEntriesResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return &pb.AppendEntriesResponse{Term: r.raftTerm, Success: false}
	}

	log.Printf("[Raft Receive] %s <- %s AppendEntries term=%d entries=%d", r.ID, req.GetLeaderId(), req.GetTerm(), len(req.GetEntries()))

	resp := &pb.AppendEntriesResponse{Term: r.raftTerm, Success: false}
	if req.GetTerm() < r.raftTerm {
		return resp
	}

	if req.GetTerm() > r.raftTerm {
		r.becomeFollowerLocked(req.GetTerm(), req.GetLeaderId())
	}

	r.knownLeaderID = req.GetLeaderId()
	r.raftState = raftFollower
	r.lastLeaderSeenAt = time.Now()

	if req.GetPrevLogIndex() >= 0 {
		if req.GetPrevLogIndex() >= int64(len(r.raftLog)) {
			return resp
		}
		if r.raftLog[req.GetPrevLogIndex()].Term != req.GetPrevLogTerm() {
			return resp
		}
	}

	insertIndex := req.GetPrevLogIndex() + 1
	for i, entry := range req.GetEntries() {
		idx := insertIndex + int64(i)
		incoming := RaftLogEntry{
			Term:            entry.GetTerm(),
			Index:           idx,
			Type:            entry.GetLogType(),
			Payload:         append([]byte(nil), entry.GetCommand()...),
			TimestampUnixMs: entry.GetTimestampUnixMs(),
		}

		if idx < int64(len(r.raftLog)) {
			existing := r.raftLog[idx]
			if existing.Term != incoming.Term || existing.Type != incoming.Type || !bytes.Equal(existing.Payload, incoming.Payload) {
				r.raftLog = r.raftLog[:idx]
				r.raftLog = append(r.raftLog, incoming)
			}
			continue
		}

		if idx > int64(len(r.raftLog)) {
			return resp
		}
		r.raftLog = append(r.raftLog, incoming)
	}

	if req.GetLeaderCommit() > r.commitIndex {
		r.commitIndex = min64(req.GetLeaderCommit(), int64(len(r.raftLog)-1))
	}
	r.applyCommittedEntriesLocked()

	resp.Term = r.raftTerm
	resp.Success = true
	return resp
}

func (r *Robot) becomeFollowerLocked(term int64, leaderID string) {
	r.raftTerm = max64(r.raftTerm, term)
	r.raftState = raftFollower
	r.knownLeaderID = leaderID
	r.votedFor = ""
	r.lastVoteRequestAt = time.Time{} // resets to zero state
	r.votesGranted = make(map[string]bool)
	r.lastLeaderSeenAt = time.Now()
	r.electionTimeout = randomElectionTimeout()
}

func (r *Robot) shouldGrantVoteLocked(candidateID string, candidateLastLogIndex int64, candidateLastLogTerm int64) bool {
	if r.votedFor != "" && r.votedFor != candidateID {
		return false
	}

	lastIdx, lastTerm := r.getLastLogInfoLocked()
	logUpToDate := candidateLastLogTerm > lastTerm ||
		(candidateLastLogTerm == lastTerm && candidateLastLogIndex >= lastIdx)

	if !logUpToDate {
		return false
	}

	r.votedFor = candidateID
	r.lastLeaderSeenAt = time.Now()
	r.electionTimeout = randomElectionTimeout()
	return true
}

func (r *Robot) getLastLogInfoLocked() (int64, int64) {
	if len(r.raftLog) == 0 {
		return -1, -1
	}
	last := r.raftLog[len(r.raftLog)-1]
	return last.Index, last.Term
}

func randomElectionTimeout() time.Duration {
	return raftElectionMinTimeout + time.Duration(rand.Int63n(int64(raftElectionJitter)))
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (r *Robot) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

func (r *Robot) setPaused(paused bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.paused == paused {
		return
	}

	r.paused = paused
	if paused {
		log.Printf("[pause] Robot %s paused", r.ID)
		return
	}
	log.Printf("[pause] Robot %s resumed", r.ID)
}
