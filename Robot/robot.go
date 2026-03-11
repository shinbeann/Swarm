package main

import (
	"context"
	"log"
	"math"
	"math/rand"
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

	// Last sensed obstacle angle (radians from robot); only valid when nearObstacle is true
	nearObstacle  bool
	obstacleAngle float64
}

func NewRobot(id string, client pb.RobotServiceClient) *Robot {
	return &Robot{
		ID:       id,
		X:        rand.Float64() * 800,
		Y:        rand.Float64() * 600,
		Heading:  rand.Float64() * 2 * math.Pi,
		Client:   client,
		Clock:    NewLamportClock(),
		lastSync: time.Now(),
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
		log.Printf("heartbeat error: %v", err)
	} else if heartbeatResp != nil && heartbeatResp.GetSuccess() {
		// Sync local position mirror from the authoritative WorldEngine state
		r.X = heartbeatResp.GetX()
		r.Y = heartbeatResp.GetY()
		r.Heading = heartbeatResp.GetHeading()
	}

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
	}

	// P2P Sync (Every 2 seconds)
	if time.Since(r.lastSync) > 2*time.Second {
		r.lastSync = time.Now()
		r.syncWithPeers(ctx)
	}
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

func (r *Robot) syncWithPeers(ctx context.Context) {
	netResp, err := r.Client.GetNetworkData(ctx, &pb.NetworkRequest{RobotId: r.ID})
	if err != nil {
		log.Printf("could not get network data: %v", err)
		return
	}

	for _, cond := range netResp.GetNetworkConditions() {
		targetID := cond.GetTargetRobotId()
		latency := cond.GetLatency()
		bandwidth := cond.GetBandwidth()
		reliability := cond.GetReliability()

		go func(tID string, lat, bw, rel float64) {
			// 1. Reliability (Packet Loss)
			if rand.Float64() > rel {
				log.Printf("[P2P Send] Packet from %s to %s DROP (Reliability: %.2f)", r.ID, tID, rel)
				return
			}

			// Simulated Payload of 1MB (8 Megabits)
			payloadSizeMB := 1.0
			transferTimeSec := (payloadSizeMB * 8.0) / bw
			transferTimeMs := transferTimeSec * 1000.0
			totalDelayMs := lat + transferTimeMs

			log.Printf("[P2P Send] %s -> %s (Delay: %.0fms, BW: %.1fMbps)", r.ID, tID, totalDelayMs, bw)

			// 2. Latency + Transfer Delay
			time.Sleep(time.Duration(totalDelayMs) * time.Millisecond)

			peerAddr := tID + ":50052"
			conn, err := grpc.NewClient(peerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return
			}
			defer conn.Close()

			peerClient := pb.NewPeerServiceClient(conn)
			syncCtx, syncCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer syncCancel()

			_, err = peerClient.SyncData(syncCtx, &pb.PeerSyncRequest{
				SenderId:     r.ID,
				Payload:      []byte{1}, // dummy payload
				LamportClock: int64(r.Clock.Time()),
			})
			if err != nil {
				log.Printf("[P2P Error] %s -> %s: %v", r.ID, tID, err)
			}
		}(targetID, latency, bandwidth, reliability)
	}
}