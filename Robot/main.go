package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type peerServer struct {
	pb.UnimplementedPeerServiceServer
	robot *Robot
}

func (s *peerServer) SyncData(ctx context.Context, req *pb.PeerSyncRequest) (*pb.PeerSyncResponse, error) {
	if s.robot != nil {
		s.robot.OnPeerSync(req)
	}
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

	robot := NewRobot(*robotID, client)

	// Start Peer gRPC server
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Control loop
	go robot.Run(ctx)

	<-sigCh
	log.Println("Shutting down robot...")
	robot.Stop()
	peerSrv.GracefulStop()
}
