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
	ID       string
	X        float64
	Y        float64
	Heading  float64
	LastSync time.Time

	Client pb.RobotServiceClient
	Clock  *LamportClock
}

func NewRobot(id string, client pb.RobotServiceClient) *Robot {
	return &Robot{
		ID:       id,
		X:        rand.Float64() * 800,
		Y:        rand.Float64() * 600,
		Heading:  rand.Float64() * 2 * math.Pi,
		LastSync: time.Now(),
		Client:   client,
		Clock:    NewLamportClock(),
	}
}

func (r *Robot) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Millisecond) // ~33 times per second
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
	_, err := r.Client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		RobotId: r.ID,
		X:       r.X,
		Y:       r.Y,
		Heading: r.Heading,
	})
	if err != nil {
		log.Printf("heartbeat error: %v", err)
	}

	// Sensor
	sensorResp, err := r.Client.GetSensorData(ctx, &pb.SensorRequest{RobotId: r.ID})
	if err != nil {
		log.Printf("could not get sensor data: %v", err)
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

	// Network Sync (Every 2 seconds)
	if time.Since(r.LastSync) > 2*time.Second {
		r.LastSync = time.Now()

		netResp, err := r.Client.GetNetworkData(ctx, &pb.NetworkRequest{RobotId: r.ID})
		if err != nil {
			log.Printf("could not get network data: %v", err)
		} else {
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
						SenderId: r.ID,
						Payload:  []byte{1}, // minimal payload; transfer delay already simulated above
					})
					if err != nil {
						log.Printf("[P2P Error] %s -> %s: %v", r.ID, tID, err)
					}
				}(targetID, latency, bandwidth, reliability)
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
	velocity := 20.0 // Units per second

	_, err = r.Client.MoveToPosition(ctx, &pb.MoveRequest{
		RobotId:  r.ID,
		X:        dirX,
		Y:        dirY,
		Velocity: velocity * 0.2,
	})
	if err != nil {
		log.Printf("could not send move request: %v", err)
	}

	r.X += dirX * (velocity * 0.2)
	r.Y += dirY * (velocity * 0.2)
	r.Heading = newHeading

	r.X = math.Max(0, math.Min(1000, r.X))
	r.Y = math.Max(0, math.Min(1000, r.Y))
}