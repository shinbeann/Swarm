package main

import (
	"context"
	"flag"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
	"net"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type peerServer struct {
	pb.UnimplementedPeerServiceServer
}

func (s *peerServer) SyncData(ctx context.Context, req *pb.PeerSyncRequest) (*pb.PeerSyncResponse, error) {
	log.Printf("[P2P Recv] Robot %s received sync from %s", *robotID, req.GetSenderId())
	return &pb.PeerSyncResponse{Received: true}, nil
}

var (
	worldEngineAddr = flag.String("world-engine", "world-engine:50051", "The address of the WorldEngine gRPC server")
	robotID         = flag.String("id", os.Getenv("ROBOT_ID"), "The unique ID for this robot")
)

func main() {
	flag.Parse()

	if *robotID == "" {
		// If no explicit ID provided via env or -id flag, fall back to container hostname.
		// This lets `docker compose --scale robot=N` start unique robots without extra envs.
		hn, err := os.Hostname()
		if err == nil && hn != "" {
			*robotID = hn
		} else if env := os.Getenv("HOSTNAME"); env != "" {
			*robotID = env
		} else {
			log.Fatal("ROBOT_ID environment variable, -id flag, or container hostname is required")
		}
	}

	log.Printf("Starting Robot %s...", *robotID)

	// Set up a connection to the world engine.
	conn, err := grpc.NewClient(*worldEngineAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewRobotServiceClient(conn)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Start Peer gRPC server
	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("failed to listen for peer connections: %v", err)
	}
	peerSrv := grpc.NewServer()
	pb.RegisterPeerServiceServer(peerSrv, &peerServer{})
	go func() {
		log.Printf("Starting Peer server on :50052...")
		if err := peerSrv.Serve(lis); err != nil {
			log.Fatalf("failed to serve peer server: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize position state (will be updated by WorldEngine)
	posX := rand.Float64() * 800
	posY := rand.Float64() * 600
	head := rand.Float64() * 2 * 3.14159

	lastSync := time.Now()

	// Control loop
	go func() {
		ticker := time.NewTicker(30 * time.Millisecond) // Move ~33 times a second
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runControlLoop(ctx, client, &posX, &posY, &head, &lastSync)
			}
		}
	}()

	<-sigCh
	log.Println("Shutting down robot...")
	peerSrv.GracefulStop()
}

func runControlLoop(ctx context.Context, client pb.RobotServiceClient, x, y, heading *float64, lastSync *time.Time) {
	// Send heartbeat
	_, err := client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		RobotId: *robotID,
		X:       *x,
		Y:       *y,
		Heading: *heading,
	})
	if err != nil {
		log.Printf("could not send heartbeat: %v", err)
	}

	// Example: get sensor data
	sensorResp, err := client.GetSensorData(ctx, &pb.SensorRequest{RobotId: *robotID})
	if err != nil {
		log.Printf("could not get sensor data: %v", err)
		return
	}

	obstacleDetected := false
	for _, obj := range sensorResp.GetObjects() {
		if obj.GetType() == "obstacle" {
			// Calculate distance to obstacle
			distX := obj.GetX() - *x
			distY := obj.GetY() - *y
			dist := math.Sqrt(distX*distX + distY*distY)

			if dist <= 5.0 {
				obstacleDetected = true
				break
			}
		}
	}
	// log.Printf("Received sensor data for %d objects", len(sensorResp.GetObjects()))

	// Note: Implement robot logic based on sensor data here.
	
	// Network Sync (Every 2 seconds)
	if time.Since(*lastSync) > 2*time.Second {
		*lastSync = time.Now()

		netResp, err := client.GetNetworkData(ctx, &pb.NetworkRequest{RobotId: *robotID})
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
						log.Printf("[P2P Send] Packet from %s to %s DROP (Reliability: %.2f)", *robotID, tID, rel)
						return
					}

					// Simulated Payload of 1MB (8 Megabits)
					payloadSizeMB := 1.0
					transferTimeSec := (payloadSizeMB * 8.0) / bw 
					transferTimeMs := transferTimeSec * 1000.0
					totalDelayMs := lat + transferTimeMs

					log.Printf("[P2P Send] %s -> %s (Delay: %.0fms, BW: %.1fMbps)", *robotID, tID, totalDelayMs, bw)

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
						SenderId: *robotID,
						Payload:  []byte{1}, // dummy payload but we pretended it was 1MB above
					})
					if err != nil {
						log.Printf("[P2P Error] %s -> %s: %v", *robotID, tID, err)
					}
				}(targetID, latency, bandwidth, reliability)
			}
		}
	}

	// Generate random movement intent (direction vector)
	// Bias it slightly towards the current heading so it doesn't just jitter in place
	angleRange := 0.5 // Radians to drift from current heading
	newAttemptHeading := *heading + (rand.Float64()*angleRange - angleRange/2.0)

	if obstacleDetected {
		// Reverse heading broadly if an obstacle is too close
		newAttemptHeading += math.Pi // add 180 degrees
		// log.Printf("Robot %s detected obstacle, turning around!", *robotID)
	}

	dirX := math.Cos(newAttemptHeading)
	dirY := math.Sin(newAttemptHeading)
	velocity := 20.0 // Units per second

	_, err = client.MoveToPosition(ctx, &pb.MoveRequest{
		RobotId:  *robotID,
		X:        dirX,
		Y:        dirY,
		Velocity: velocity * 0.2, // Applying time delta for 5 FPS approximately = 0.2s
	})
	if err != nil {
		log.Printf("could not send move request: %v", err)
	}

	// We'll update local client state mathematically.
	// For simulation accuracy, the WORLD specifies where the robot *actually* is.
	// A simpler way for this project is to update locally, since WorldEngine bounds check it anyway and we sync at same rate.

	*x += dirX * (velocity * 0.2)
	*y += dirY * (velocity * 0.2)
	*heading = newAttemptHeading

	// Clamp locally to avoid divergence from world engine before the next sync
	*x = math.Max(0, math.Min(1000, *x))
	*y = math.Max(0, math.Min(1000, *y))
}
