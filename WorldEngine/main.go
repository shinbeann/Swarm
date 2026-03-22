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
	robotSpeed      = 50.0
	simTickMs       = 30
	simDt           = simTickMs / 1000.0
	discoveryRadius = 10.0  // must be within this distance to detect a landmark
)

// -------------------------------------------------------------------------
// Landmark — new type owned by the world engine
// -------------------------------------------------------------------------

// WorldLandmark is a point of interest placed by the world engine at startup.
// Robots discover it when they come within discoveryRadius.
type WorldLandmark struct {
	ID       LandmarkID
	Type     LandmarkType
	Location Location
}

// -------------------------------------------------------------------------
// RobotState
// -------------------------------------------------------------------------

type RobotState struct {
	Info     *pb.RobotInfo
	LastSeen time.Time

	HasTarget     bool
	TargetX       float64
	TargetY       float64
	TargetHeading float64
}

// -------------------------------------------------------------------------
// server
// -------------------------------------------------------------------------

type server struct {
	pb.UnimplementedRobotServiceServer
	pb.UnimplementedVisualiserServiceServer

	mu     sync.RWMutex
	robots map[string]*RobotState
	walls  []*pb.Obstacle

	landmarks map[LandmarkID]*WorldLandmark
}

// -------------------------------------------------------------------------
// newServer — replaces the inline &server{} literal in main()
// -------------------------------------------------------------------------

func newServer() *server {
	s := &server{
		robots: make(map[string]*RobotState),
		walls: []*pb.Obstacle{
			{Id: "wall-top", X: 0, Y: -10, Width: 1000, Height: 10},
			{Id: "wall-bottom", X: 0, Y: 1000, Width: 1000, Height: 10},
			{Id: "wall-left", X: -10, Y: 0, Width: 10, Height: 1000},
			{Id: "wall-right", X: 1000, Y: 0, Width: 10, Height: 1000},
		},
		landmarks: make(map[LandmarkID]*WorldLandmark),
	}
	s.spawnLandmarks()
	return s
}

// spawnLandmarks places a fixed set of landmarks into the world at startup.
// Swap the hardcoded positions for random generation if preferred.
func (s *server) spawnLandmarks() {
	fixed := []struct {
		id   LandmarkID
		t    LandmarkType
		x, y float64
	}{
		{"casualty-0", LandmarkCasualty, 200, 300},
		{"casualty-1", LandmarkCasualty, 700, 650},
		{"casualty-2", LandmarkCasualty, 450, 800},
		{"corridor-0", LandmarkCorridor, 500, 500},
		{"corridor-1", LandmarkCorridor, 150, 750},
		{"obstacle-0", LandmarkObstacle, 300, 200},
		{"obstacle-1", LandmarkObstacle, 800, 400},
	}

	for _, f := range fixed {
		s.landmarks[f.id] = &WorldLandmark{
			ID:   f.id,
			Type: f.t,
			Location: Location{X: f.x, Y: f.y},
		}
	}

	log.Printf("[world] spawned %d landmarks", len(s.landmarks))
}

// -------------------------------------------------------------------------
// Existing RobotService handlers — only SendHeartbeat changes
// -------------------------------------------------------------------------

func (s *server) SendHeartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
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

	state.LastSeen = time.Now()

	return &pb.HeartbeatResponse{
		Success: true,
		X:       state.Info.X,
		Y:       state.Info.Y,
		Heading: state.Info.Heading,
	}, nil
}

// MoveToPosition — unchanged from your original.
func (s *server) MoveToPosition(ctx context.Context, req *pb.MoveRequest) (*pb.MoveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
		return &pb.MoveResponse{Success: false}, nil
	}

	state.TargetX = math.Max(0, math.Min(1000, req.GetTargetX()))
	state.TargetY = math.Max(0, math.Min(1000, req.GetTargetY()))
	state.TargetHeading = req.GetDesiredHeading()
	state.HasTarget = true

	log.Printf("Robot %s new target: (%.1f, %.1f) heading %.2frad",
		req.GetRobotId(), state.TargetX, state.TargetY, state.TargetHeading)

	return &pb.MoveResponse{Success: true}, nil
}

// GetSensorData — detects obstacles and landmarks within sensor range.
func (s *server) GetSensorData(ctx context.Context, req *pb.SensorRequest) (*pb.SensorResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, exists := s.robots[req.GetRobotId()]
	if !exists {
		return &pb.SensorResponse{}, nil
	}

	var detected []*pb.ObjectData
	sensorRange := 10.0

	// Detect obstacles (walls)
	for _, w := range s.walls {
		closestX := math.Max(w.X, math.Min(state.Info.X, w.X+w.Width))
		closestY := math.Max(w.Y, math.Min(state.Info.Y, w.Y+w.Height))
		distX := state.Info.X - closestX
		distY := state.Info.Y - closestY
		if (distX*distX)+(distY*distY) <= sensorRange*sensorRange {
			detected = append(detected, &pb.ObjectData{
				Id:   w.Id,
				X:    closestX,
				Y:    closestY,
				Type: "obstacle",
			})
		}
	}

	// Detect landmarks within discoveryRadius
	for id, landmark := range s.landmarks {
		distX := state.Info.X - landmark.Location.X
		distY := state.Info.Y - landmark.Location.Y
		dist := math.Sqrt(distX*distX + distY*distY)

		if dist <= discoveryRadius {
			// Return landmark as ObjectData with type indicating landmark
			detected = append(detected, &pb.ObjectData{
				Id:   string(id),
				X:    landmark.Location.X,
				Y:    landmark.Location.Y,
				Type: "landmark:" + string(landmark.Type),
			})
		}
	}

	return &pb.SensorResponse{Objects: detected}, nil
}

// GetNetworkData — unchanged from your original.
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
			continue
		}
		distX := reqState.Info.X - state.Info.X
		distY := reqState.Info.Y - state.Info.Y
		distance := math.Sqrt(distX*distX + distY*distY)

		if distance <= maxRange {
			ratio := distance / maxRange
			conditions = append(conditions, &pb.NetworkData{
				TargetRobotId: id,
				Bandwidth:     50.0 - (49.0 * ratio),
				Latency:       5.0 + (245.0 * ratio),
				Reliability:   1.0 - (0.5 * ratio),
			})
		}
	}

	return &pb.NetworkResponse{NetworkConditions: conditions}, nil
}

// -------------------------------------------------------------------------
// VisualiserService handlers
// -------------------------------------------------------------------------

func (s *server) GetEnvironmentData(ctx context.Context, req *pb.EnvironmentRequest) (*pb.EnvironmentResponse, error) {
	obstacles := make([]*pb.Obstacle, 0, len(s.walls)+len(s.landmarks))
	obstacles = append(obstacles, s.walls...)

	// Encode landmarks as tiny environment obstacles so the existing Visualiser
	// payload can carry them without protobuf changes.
	for _, lm := range s.landmarks {
		obstacles = append(obstacles, &pb.Obstacle{
			Id:     "landmark:" + string(lm.ID) + ":" + string(lm.Type),
			X:      lm.Location.X - 4,
			Y:      lm.Location.Y - 4,
			Width:  8,
			Height: 8,
		})
	}

	return &pb.EnvironmentResponse{
		Width:     1000.0,
		Height:    1000.0,
		Obstacles: obstacles,
	}, nil
}

func (s *server) GetRobotData(ctx context.Context, req *pb.RobotDataRequest) (*pb.RobotDataResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rbts []*pb.RobotInfo
	for _, state := range s.robots {
		rbts = append(rbts, state.Info)
	}
	return &pb.RobotDataResponse{Robots: rbts}, nil
}

// -------------------------------------------------------------------------
// Background loops
// -------------------------------------------------------------------------

// runSimTick steps every robot toward its movement target.
// Identical to your original — no changes needed.
func (s *server) runSimTick() {
	ticker := time.NewTicker(simTickMs * time.Millisecond)
	for range ticker.C {
		s.mu.Lock()
		for _, state := range s.robots {
			if !state.HasTarget {
				continue
			}
			dx := state.TargetX - state.Info.X
			dy := state.TargetY - state.Info.Y
			dist := math.Sqrt(dx*dx + dy*dy)
			step := robotSpeed * simDt
			if dist <= step {
				state.Info.X = state.TargetX
				state.Info.Y = state.TargetY
				state.Info.Heading = state.TargetHeading
				state.HasTarget = false
			} else {
				ratio := step / dist
				newX := state.Info.X + dx*ratio
				newY := state.Info.Y + dy*ratio
				newX = math.Max(0, math.Min(1000, newX))
				newY = math.Max(0, math.Min(1000, newY))
				if newX != state.Info.X+dx*ratio || newY != state.Info.Y+dy*ratio {
					state.HasTarget = false
				}
				state.Info.X = newX
				state.Info.Y = newY
				state.Info.Heading = math.Atan2(dy, dx)
			}
		}
		s.mu.Unlock()
	}
}

// runCleanupTick removes robots that have stopped heartbeating.
// Identical to your original — no changes needed.
func (s *server) runCleanupTick() {
	ticker := time.NewTicker(2 * time.Second)
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for id, state := range s.robots {
			if now.Sub(state.LastSeen) > 5*time.Second {
				log.Printf("Robot %s timed out, removing from world", id)
				delete(s.robots, id)
			}
		}
		s.mu.Unlock()
	}
}

// -------------------------------------------------------------------------
// main
// -------------------------------------------------------------------------

func main() {
	flag.Parse()

	log.Printf("Starting World Engine on port %s...", *port)

	lis, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	srv := newServer() // replaces the inline &server{} literal

	go srv.runSimTick()
	go srv.runCleanupTick()

	pb.RegisterRobotServiceServer(grpcServer, srv)
	pb.RegisterVisualiserServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down World Engine...")
	grpcServer.GracefulStop()
}