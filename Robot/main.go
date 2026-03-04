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

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	robot := NewRobot(*robotID, client)

	// Control loop
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond) // Move 5 times a second
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				robot.Run(ctx)
			}
		}
	}()

	<-sigCh
	log.Println("Shutting down robot...")
}

func runControlLoop(ctx context.Context, client pb.RobotServiceClient, x, y, heading *float64) {
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
	// Communicate with other robots, etc.

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
	velocity := 50.0 // Units per second

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
