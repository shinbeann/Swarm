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

const (
	robotSpeed = 50.0  // units per second
	simTickMs  = 30   // simulation tick in milliseconds
	simDt      = simTickMs / 1000.0 // seconds per tick
)

// RobotState tracks the canonical position and movement target for a robot.
type RobotState struct {
	Info     *pb.RobotInfo
	LastSeen time.Time

	// Current movement target set by the robot
	HasTarget     bool
	TargetX       float64
	TargetY       float64
	TargetHeading float64
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
		return &pb.MoveResponse{Success: false}, nil // Robot not yet registered via heartbeat
	}

	// Clamp target to world bounds
	state.TargetX = math.Max(0, math.Min(1000, req.GetTargetX()))
	state.TargetY = math.Max(0, math.Min(1000, req.GetTargetY()))
	state.TargetHeading = req.GetDesiredHeading()
	state.HasTarget = true

	log.Printf("Robot %s new target: (%.1f, %.1f) heading %.2frad",
		req.GetRobotId(), state.TargetX, state.TargetY, state.TargetHeading)

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

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
		// First heartbeat: seed initial position from robot's reported spawn location
		state = &RobotState{
			Info: &pb.RobotInfo{
				Id:      req.GetRobotId(),
				X:       math.Max(0, math.Min(1000, req.GetX())),
				Y:       math.Max(0, math.Min(1000, req.GetY())),
				Heading: req.GetHeading(),
			},
		}
		s.robots[req.GetRobotId()] = state
		log.Printf("Robot %s registered at (%.1f, %.1f)", req.GetRobotId(), state.Info.X, state.Info.Y)
	}

	// Always refresh LastSeen; the WorldEngine owns X/Y/Heading after initial registration
	state.LastSeen = time.Now()

	return &pb.HeartbeatResponse{
		Success: true,
		X:       state.Info.X,
		Y:       state.Info.Y,
		Heading: state.Info.Heading,
	}, nil
}

func (s *server) GetNetworkData(ctx context.Context, req *pb.NetworkRequest) (*pb.NetworkResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	reqRobotID := req.GetRobotId()
	reqState, exists := s.robots[reqRobotID]
	if !exists {
		return &pb.NetworkResponse{NetworkConditions: []*pb.NetworkData{}}, nil
	}

	var conditions []*pb.NetworkData
	maxRange := 250.0

	for id, state := range s.robots {
		if id == reqRobotID {
			continue // Skip self
		}

		distX := reqState.Info.X - state.Info.X
		distY := reqState.Info.Y - state.Info.Y
		distance := math.Sqrt(distX*distX + distY*distY)

		if distance <= maxRange {
			// Calculate degradation based on distance ratio (0.0 to 1.0)
			ratio := distance / maxRange

			// Bandwidth: 50.0 to 1.0 Mbps
			bandwidth := 50.0 - (49.0 * ratio)
			// Latency: 5.0 to 250.0 ms
			latency := 5.0 + (245.0 * ratio)
			// Reliability: 1.0 to 0.5
			reliability := 1.0 - (0.5 * ratio)

			conditions = append(conditions, &pb.NetworkData{
				TargetRobotId: id,
				Bandwidth:     bandwidth,
				Latency:       latency,
				Reliability:   reliability,
			})
		}
	}

	return &pb.NetworkResponse{
		NetworkConditions: conditions,
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

	// Simulation tick: step every robot toward its target at robotSpeed units/sec
	go func() {
		ticker := time.NewTicker(simTickMs * time.Millisecond)
		for range ticker.C {
			srv.mu.Lock()
			for _, state := range srv.robots {
				if !state.HasTarget {
					continue
				}
				dx := state.TargetX - state.Info.X
				dy := state.TargetY - state.Info.Y
				dist := math.Sqrt(dx*dx + dy*dy)
				step := robotSpeed * simDt
				if dist <= step {
					// Close enough — snap to target
					state.Info.X = state.TargetX
					state.Info.Y = state.TargetY
					state.Info.Heading = state.TargetHeading
					state.HasTarget = false
				} else {
					// Advance one step along the vector toward target
					ratio := step / dist
					newX := state.Info.X + dx*ratio
					newY := state.Info.Y + dy*ratio
					// Wall collision: clamp and cancel target if boundary reached
					newX = math.Max(0, math.Min(1000, newX))
					newY = math.Max(0, math.Min(1000, newY))
					if newX != state.Info.X+dx*ratio || newY != state.Info.Y+dy*ratio {
						// Robot hit a boundary wall — stop here
						state.HasTarget = false
					}
					state.Info.X = newX
					state.Info.Y = newY
					state.Info.Heading = math.Atan2(dy, dx)
				}
			}
			srv.mu.Unlock()
		}
	}()

	// Cleanup tick: remove robots that have stopped sending heartbeats
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
