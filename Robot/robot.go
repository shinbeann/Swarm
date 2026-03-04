package main

import (
	"context"
	"log"
	"math"
	"math/rand"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
)

type Robot struct {
	ID      string
	X       float64
	Y       float64
	Heading float64

	Client pb.RobotServiceClient
	Clock  *LamportClock
}

func NewRobot(id string, client pb.RobotServiceClient) *Robot {
	return &Robot{
		ID:      id,
		X:       rand.Float64() * 800,
		Y:       rand.Float64() * 600,
		Heading: rand.Float64() * 2 * math.Pi,
		Client:  client,
		Clock:   NewLamportClock(),
	}
}

func (r *Robot) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
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
}