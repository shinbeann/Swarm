package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Robot struct {
	ID      string
	X       float64
	Y       float64
	Heading float64

	Client   pb.RobotServiceClient
	Clock    *LamportClock
	lastSync time.Time

	store               *KnowledgeStore
	neighbours          *NeighbourRegistry
	networkMu           sync.RWMutex
	networkConditions   map[RobotID]*pb.NetworkData
	gossip              *GossipEngine
	discoveredLandmarks map[LandmarkID]bool

	// Last sensed obstacle angle (radians from robot); only valid when nearObstacle is true
	nearObstacle  bool
	obstacleAngle float64
}

func NewRobot(id string, client pb.RobotServiceClient) *Robot {
	r := &Robot{
		ID:                  id,
		X:                   rand.Float64() * 800,
		Y:                   rand.Float64() * 600,
		Heading:             rand.Float64() * 2 * math.Pi,
		Client:              client,
		Clock:               NewLamportClock(),
		lastSync:            time.Now(),
		store:               NewKnowledgeStore(),
		neighbours:          NewNeighbourRegistry(3 * time.Second),
		networkConditions:   make(map[RobotID]*pb.NetworkData),
		discoveredLandmarks: make(map[LandmarkID]bool),
	}

	r.gossip = NewGossipEngine(r, r.sendGossipMessage)
	r.gossip.Start()
	return r
}

func (r *Robot) Stop() {
	if r.gossip != nil {
		r.gossip.Stop()
	}
}

func (r *Robot) Run(ctx context.Context) {
	heartbeatTicker := time.NewTicker(30 * time.Millisecond)
	moveTicker := time.NewTicker(2 * time.Second)
	defer heartbeatTicker.Stop()
	defer moveTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			r.tick(ctx)
		case <-moveTicker.C:
			r.requestMovement(ctx)
		}
	}
}

func (r *Robot) tick(ctx context.Context) {
	r.Clock.Tick()

	// Heartbeat — WorldEngine returns the canonical position
	heartbeatResp, err := r.Client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		RobotId: r.ID,
		X:       r.X,
		Y:       r.Y,
		Heading: r.Heading,
	})
	if err != nil {
		log.Printf("worldheartbeat error: %v", err)
	} else if heartbeatResp != nil && heartbeatResp.GetSuccess() {
		// Sync local position mirror from the authoritative WorldEngine state
		r.X = heartbeatResp.GetX()
		r.Y = heartbeatResp.GetY()
		r.Heading = heartbeatResp.GetHeading()
	}

	r.refreshNetworkConditions(ctx)

	// Sensor — detect nearby obstacles; result is used by requestMovement
	sensorResp, err := r.Client.GetSensorData(ctx, &pb.SensorRequest{RobotId: r.ID})
	if err != nil {
		return
	}

	r.nearObstacle = false
	for _, obj := range sensorResp.GetObjects() {
		if obj.GetType() == "obstacle" {
			distX := obj.GetX() - r.X
			distY := obj.GetY() - r.Y
			dist := math.Sqrt(distX*distX + distY*distY)
			if dist <= 5.0 {
				r.nearObstacle = true
				r.obstacleAngle = math.Atan2(distY, distX)
				break
			}
		}

		if strings.HasPrefix(obj.GetType(), "landmark:") {
			id := LandmarkID(obj.GetId())
			if !r.discoveredLandmarks[id] {
				r.discoveredLandmarks[id] = true
				ltype := LandmarkType(strings.TrimPrefix(obj.GetType(), "landmark:"))
				r.gossip.RecordDiscovery(id, ltype, Location{X: obj.GetX(), Y: obj.GetY()})
			}
		}
	}
}

func (r *Robot) refreshNetworkConditions(ctx context.Context) {
	networkResp, err := r.Client.GetNetworkData(ctx, &pb.NetworkRequest{RobotId: r.ID})
	if err != nil {
		log.Printf("world network data error: %v", err)
		return
	}

	conditions := networkResp.GetNetworkConditions()
	r.networkMu.Lock()
	r.networkConditions = make(map[RobotID]*pb.NetworkData, len(conditions))
	for _, cond := range conditions {
		targetID := RobotID(cond.GetTargetRobotId())
		r.networkConditions[targetID] = cond
		r.neighbours.RecordHeartbeat(targetID)
	}
	r.networkMu.Unlock()
}

func (r *Robot) activeNeighbours() []RobotID {
	return r.neighbours.GetActive()
}

func (r *Robot) networkCondition(id RobotID) (*pb.NetworkData, bool) {
	r.networkMu.RLock()
	defer r.networkMu.RUnlock()
	cond, ok := r.networkConditions[id]
	return cond, ok
}

// requestMovement picks a target position and asks the WorldEngine to move the robot there.
// The WorldEngine has full authority over the actual path and final position.
func (r *Robot) requestMovement(ctx context.Context) {
	// Pick a random target 100–300 units away
	distance := 100.0 + rand.Float64()*200.0
	angle := rand.Float64() * 2 * math.Pi

	if r.nearObstacle {
		// Aim away from the detected obstacle with a small random spread
		angle = r.obstacleAngle + math.Pi + (rand.Float64()*0.5 - 0.25)
	}

	targetX := r.X + math.Cos(angle)*distance
	targetY := r.Y + math.Sin(angle)*distance

	// Keep well clear of boundary walls
	targetX = math.Max(50, math.Min(950, targetX))
	targetY = math.Max(50, math.Min(950, targetY))

	desiredHeading := math.Atan2(targetY-r.Y, targetX-r.X)

	_, err := r.Client.MoveToPosition(ctx, &pb.MoveRequest{
		RobotId:        r.ID,
		TargetX:        targetX,
		TargetY:        targetY,
		DesiredHeading: desiredHeading,
	})
	if err != nil {
		log.Printf("move request error: %v", err)
	}
}

func (r *Robot) sendGossipMessage(to RobotID, msg *GossipMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal gossip payload: %w", err)
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
	r.Clock.Update(int(req.GetLamportClock()))
	r.neighbours.RecordHeartbeat(RobotID(req.GetSenderId()))
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
