package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type peerServer struct {
	pb.UnimplementedPeerServiceServer
	robot *Robot
}

type raftServer struct {
	pb.UnimplementedRaftServiceServer
	robot *Robot
}

// SyncData handles incoming gossip knowledge payloads and Lamport clock updates
// over PeerService. Actual landmark merging is delegated to robot.HandlePeerSync.
func (s *peerServer) SyncData(ctx context.Context, req *pb.PeerSyncRequest) (*pb.PeerSyncResponse, error) {
	if s.robot == nil {
		return &pb.PeerSyncResponse{Received: true}, nil
	}
	return s.robot.HandlePeerSync(req), nil
}

func (s *raftServer) RequestVote(ctx context.Context, req *pb.VoteRequest) (*pb.VoteResponse, error) {
	if s.robot == nil {
		return &pb.VoteResponse{}, nil
	}
	return s.robot.HandleRequestVote(req), nil
}

func (s *raftServer) AppendEntries(ctx context.Context, req *pb.AppendEntriesRequest) (*pb.AppendEntriesResponse, error) {
	if s.robot == nil {
		return &pb.AppendEntriesResponse{}, nil
	}
	return s.robot.HandleAppendEntries(req), nil
}

var (
	worldEngineAddr = flag.String("world-engine", "world-engine:50051", "The address of the WorldEngine gRPC server")
	robotID         = flag.String("id", os.Getenv("ROBOT_ID"), "The unique ID for this robot")
	raftAddr        = flag.String("raft-addr", ":50053", "gRPC address for dedicated Raft service")
	statusAddr      = flag.String("status-addr", ":8081", "HTTP address for robot status endpoint")
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

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	robot := NewRobot(*robotID, client)

	// Start Peer gRPC server — gossip knowledge dissemination over PeerService.
	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("failed to listen for peer connections: %v", err)
	}
	peerSrv := grpc.NewServer()
	pb.RegisterPeerServiceServer(peerSrv, &peerServer{robot: robot})
	go func() {
		log.Printf("Starting Peer server on :50052...")
		if err := peerSrv.Serve(lis); err != nil {
			log.Fatalf("failed to serve peer server: %v", err)
		}
	}()

	// Start Raft gRPC server — leader election and log replication over RaftService.
	// Kept strictly separate from PeerService so consensus and gossip do not intermesh.
	raftLis, err := net.Listen("tcp", *raftAddr)
	if err != nil {
		log.Fatalf("failed to listen for raft connections: %v", err)
	}
	raftSrv := grpc.NewServer()
	pb.RegisterRaftServiceServer(raftSrv, &raftServer{robot: robot})
	go func() {
		log.Printf("Starting Raft server on %s...", *raftAddr)
		if err := raftSrv.Serve(raftLis); err != nil {
			log.Fatalf("failed to serve raft server: %v", err)
		}
	}()

	// Start HTTP status server — exposes both gossip and Raft state via GET /status.
	statusMux := http.NewServeMux()
	statusMux.HandleFunc("/status", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(robot.StatusSnapshot()); err != nil {
			http.Error(w, "failed to encode status", http.StatusInternalServerError)
			return
		}
	})
	statusSrv := &http.Server{
		Addr:    *statusAddr,
		Handler: statusMux,
	}
	go func() {
		log.Printf("Starting status server on %s", *statusAddr)
		if err := statusSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("failed to serve status server: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Control loop
	go robot.Run(ctx)

	<-sigCh
	log.Println("Shutting down robot...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := statusSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("status server shutdown error: %v", err)
	}
	raftSrv.GracefulStop()
	robot.Stop()
	peerSrv.GracefulStop()
}
