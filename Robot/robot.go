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
	ticker := time.NewTicker(30 * time.Millisecond) // Move ~33 times a second
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Robot) tick(ctx context.Context) {
	r.Clock.Tick()

	// Heartbeat
	heartbeatResp, err := r.Client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		RobotId: r.ID,
		X:       r.X,
		Y:       r.Y,
		Heading: r.Heading,
	})
	if err != nil {
		log.Printf("heartbeat error: %v", err)
	} else if heartbeatResp != nil {
		// Optional: update clock if world returns a clock value, 
		// but the proto currently doesn't seem to have it in HeartbeatResponse.
	}

	// Sensor
	sensorResp, err := r.Client.GetSensorData(ctx, &pb.SensorRequest{RobotId: r.ID})
	if err != nil {
		return
	}

	obstacleDetected := false
	for _, obj := range sensorResp.GetObjects() {
		if obj.GetType() == "obstacle" {
			distX := obj.GetX() - r.X
			distY := obj.GetY() - r.Y
			dist := math.Sqrt(distX*distX + distY*distY)
			if dist <= 5.0 {
				obstacleDetected = true
				break
			}
		}
	}

	angleRange := 0.5
	newHeading := r.Heading + (rand.Float64()*angleRange - angleRange/2.0)

	if obstacleDetected {
		newHeading += math.Pi
	}

	dirX := math.Cos(newHeading)
	dirY := math.Sin(newHeading)
	velocity := 50.0

	_, _ = r.Client.MoveToPosition(ctx, &pb.MoveRequest{
		RobotId:  r.ID,
		X:        dirX,
		Y:        dirY,
		Velocity: velocity * 0.2,
	})

	r.X += dirX * (velocity * 0.2)
	r.Y += dirY * (velocity * 0.2)
	r.Heading = newHeading

	r.X = math.Max(0, math.Min(1000, r.X))
	r.Y = math.Max(0, math.Min(1000, r.Y))

	// P2P Sync (Every 2 seconds)
	if time.Since(r.lastSync) > 2*time.Second {
		r.lastSync = time.Now()
		r.syncWithPeers(ctx)
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