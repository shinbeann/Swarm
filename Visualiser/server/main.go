package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	worldEngineAddr = flag.String("world-engine", "world-engine:50051", "The address of the WorldEngine gRPC server")
	port            = flag.String("port", "8080", "The server port")
	staticDir       = flag.String("static-dir", "./ui/dist", "Directory of static web assets to serve")
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // Allow all origins for simplicity
}

func main() {
	flag.Parse()

	target := *worldEngineAddr
	if !strings.Contains(target, ":///") {
		// Force DNS resolver for container hostnames such as "world-engine:50051".
		target = "dns:///" + target
	}

	log.Printf("Connecting to World Engine at %s...", target)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("did not connect to World Engine: %v", err)
	}
	defer conn.Close()

	client := pb.NewVisualiserServiceClient(conn)

	// Expose WebSocket route
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(client, w, r)
	})

	// Serve the static frontend files
	fs := http.FileServer(http.Dir(*staticDir))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fs.ServeHTTP(w, r)
	})

	log.Printf("Starting Visualiser Go proxy on port %s...", *port)
	if err := http.ListenAndServe(":"+*port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func serveWs(client pb.VisualiserServiceClient, w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer ws.Close()

	// Control loop to fetch data from WorldEngine and stream to UI
	ticker := time.NewTicker(33 * time.Millisecond) // 30 FPS update rate for example
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Fetch environment data
			envResp, err := client.GetEnvironmentData(context.Background(), &pb.EnvironmentRequest{})
			if err != nil {
				log.Printf("Error fetching environment data: %v", err)
				continue
			}

			// Fetch robot data
			robResp, err := client.GetRobotData(context.Background(), &pb.RobotDataRequest{})
			if err != nil {
				log.Printf("Error fetching robot data: %v", err)
				continue
			}

			// Combine and send to WebSocket client
			payload := map[string]interface{}{
				"environment": envResp,
				"robots":      robResp.GetRobots(),
			}

			if err := ws.WriteJSON(payload); err != nil {
				log.Printf("Error writing to websocket: %v", err)
				return // break out if client disconnected
			}
		}
	}
}
