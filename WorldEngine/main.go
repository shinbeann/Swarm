package main

import (
	"context"
	"flag"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var (
	port = flag.String("port", "50051", "The server port")
)

// RobotState tracks the last known info and heartbeat for a robot
type RobotState struct {
	Info     *pb.RobotInfo
	LastSeen time.Time
}

// server is used to implement both RobotService and VisualiserService.
type server struct {
	pb.UnimplementedRobotServiceServer
	pb.UnimplementedVisualiserServiceServer

	mu     sync.RWMutex
	robots map[string]*RobotState
	walls  []*pb.Obstacle
}

// Implement RobotService
func (s *server) MoveToPosition(ctx context.Context, req *pb.MoveRequest) (*pb.MoveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
		return &pb.MoveResponse{Success: false}, nil // Robot not tracked yet
	}

	// Calculate new position based on direction vector and velocity
	// Assume velocity is units per second, and this is called once per second
	// Ensure velocity is capped for realism
	velocity := math.Min(req.GetVelocity(), 50.0) // Max 50 units/sec

	// Update position
	state.Info.X += req.GetX() * velocity
	state.Info.Y += req.GetY() * velocity

	// Calculate new heading (in radians)
	state.Info.Heading = math.Atan2(req.GetY(), req.GetX())

	// Ensure bounds (0 to 1000)
	state.Info.X = math.Max(0, math.Min(1000, state.Info.X))
	state.Info.Y = math.Max(0, math.Min(1000, state.Info.Y))

	state.LastSeen = time.Now()

	// log.Printf("Robot %s moved to (%f, %f)", req.GetRobotId(), state.Info.X, state.Info.Y)
	return &pb.MoveResponse{Success: true}, nil
}

func (s *server) GetSensorData(ctx context.Context, req *pb.SensorRequest) (*pb.SensorResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
		return &pb.SensorResponse{}, nil
	}

	var detected []*pb.ObjectData
	sensorRange := 10.0 // 10 units

	// Check if walls are within sensor range
	// Using a simple bounding box check distance approximation for walls
	for _, w := range s.walls {
		// Calculate closest point on the rectangle to the robot's circle
		closestX := math.Max(w.X, math.Min(state.Info.X, w.X+w.Width))
		closestY := math.Max(w.Y, math.Min(state.Info.Y, w.Y+w.Height))

		distX := state.Info.X - closestX
		distY := state.Info.Y - closestY
		distanceSquared := (distX * distX) + (distY * distY)

		if distanceSquared <= (sensorRange * sensorRange) {
			detected = append(detected, &pb.ObjectData{
				Id:   w.Id,
				X:    closestX,
				Y:    closestY,
				Type: "obstacle",
			})
		}
	}

	return &pb.SensorResponse{
		Objects: detected,
	}, nil
}

func (s *server) SendHeartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.robots[req.GetRobotId()] = &RobotState{
		Info: &pb.RobotInfo{
			Id:      req.GetRobotId(),
			X:       req.GetX(),
			Y:       req.GetY(),
			Heading: req.GetHeading(),
		},
		LastSeen: time.Now(),
	}

	return &pb.HeartbeatResponse{Success: true}, nil
}

func (s *server) GetNetworkData(ctx context.Context, req *pb.NetworkRequest) (*pb.NetworkResponse, error) {
	return &pb.NetworkResponse{
		NetworkConditions: []*pb.NetworkData{},
	}, nil
}

// Implement VisualiserService
func (s *server) GetEnvironmentData(ctx context.Context, req *pb.EnvironmentRequest) (*pb.EnvironmentResponse, error) {
	return &pb.EnvironmentResponse{
		Width:     1000.0,
		Height:    1000.0,
		Obstacles: s.walls,
	}, nil
}

func (s *server) GetRobotData(ctx context.Context, req *pb.RobotDataRequest) (*pb.RobotDataResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rbts []*pb.RobotInfo
	for _, state := range s.robots {
		rbts = append(rbts, state.Info)
	}

	return &pb.RobotDataResponse{
		Robots: rbts,
	}, nil
}

func main() {
	flag.Parse()

	log.Printf("Starting World Engine on port %s...", *port)

	lis, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	srv := &server{
		robots: make(map[string]*RobotState),
		walls: []*pb.Obstacle{
			{Id: "wall-top", X: 0, Y: -10, Width: 1000, Height: 10},
			{Id: "wall-bottom", X: 0, Y: 1000, Width: 1000, Height: 10},
			{Id: "wall-left", X: -10, Y: 0, Width: 10, Height: 1000},
			{Id: "wall-right", X: 1000, Y: 0, Width: 10, Height: 1000},
		},
	}

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		for range ticker.C {
			srv.mu.Lock()
			now := time.Now()
			for id, state := range srv.robots {
				if now.Sub(state.LastSeen) > 5*time.Second {
					log.Printf("Robot %s timed out, removing from world", id)
					delete(srv.robots, id)
				}
			}
			srv.mu.Unlock()
		}
	}()

	pb.RegisterRobotServiceServer(grpcServer, srv)
	pb.RegisterVisualiserServiceServer(grpcServer, srv)

	// Register reflection service on gRPC server to allow inspection
	reflection.Register(grpcServer)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down World Engine...")
	grpcServer.GracefulStop()
}
