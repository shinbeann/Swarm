package main

import (
	"context"
	"errors"
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

type wsControlMessage struct {
	Type        string                  `json:"type"`
	Pause       *bool                   `json:"pause,omitempty"`
	RobotID     string                  `json:"robot_id,omitempty"`
	Assignments []wsPartitionAssignment `json:"assignments,omitempty"`
}

type wsPartitionAssignment struct {
	RobotID    string `json:"robot_id"`
	GroupIndex uint32 `json:"group_index"`
}

type wsLeaderLogEntry struct {
	CurrentLeader   string `json:"current_leader"`
	Term            int64  `json:"term"`
	Index           int64  `json:"index"`
	Message         string `json:"message"`
	Status          int32  `json:"status"`
	TimestampUnixMs int64  `json:"timestamp_unix_ms"`
}

type wsLeaderLogData struct {
	CurrentLeader string             `json:"current_leader"`
	CurrentTerm   int64              `json:"current_term"`
	Entries       []wsLeaderLogEntry `json:"entries"`
}

func toWSLeaderLogData(resp *pb.LeaderLogResponse) wsLeaderLogData {
	if resp == nil {
		return wsLeaderLogData{}
	}

	entries := make([]wsLeaderLogEntry, 0, len(resp.GetEntries()))
	for _, entry := range resp.GetEntries() {
		if entry == nil {
			continue
		}

		entries = append(entries, wsLeaderLogEntry{
			CurrentLeader:   entry.GetCurrentLeader(),
			Term:            entry.GetTerm(),
			Index:           entry.GetIndex(),
			Message:         entry.GetMessage(),
			Status:          int32(entry.GetStatus()),
			TimestampUnixMs: entry.GetTimestampUnixMs(),
		})
	}

	return wsLeaderLogData{
		CurrentLeader: resp.GetCurrentLeader(),
		CurrentTerm:   resp.GetCurrentTerm(),
		Entries:       entries,
	}
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
	ticker := time.NewTicker(33 * time.Millisecond) // 30 FPS update rate
	defer ticker.Stop()

	controlCh := make(chan wsControlMessage)
	readErrCh := make(chan error, 1)
	latestRobotIDs := make([]string, 0)

	resolveRobotID := func(prefix string) (string, error) {
		matches := make([]string, 0, 1)
		for _, id := range latestRobotIDs {
			if strings.HasPrefix(id, prefix) {
				matches = append(matches, id)
			}
		}

		if len(matches) == 0 {
			return "", errors.New("no robot matches the provided id")
		}
		if len(matches) > 1 {
			return "", errors.New("robot id prefix is ambiguous")
		}
		return matches[0], nil
	}

	go func() {
		for {
			var msg wsControlMessage
			if err := ws.ReadJSON(&msg); err != nil {
				readErrCh <- err
				return
			}
			controlCh <- msg
		}
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case err := <-readErrCh:
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, websocket.ErrCloseSent) {
				return
			}
			log.Printf("Websocket read error: %v", err)
			return
		case msg := <-controlCh:
			switch msg.Type {
			case "set_pause":
				if msg.Pause == nil {
					continue
				}

				pauseResp, err := client.SetSimulationPause(context.Background(), &pb.SimulationPauseRequest{Pause: *msg.Pause})
				controlPayload := map[string]interface{}{
					"type":      "set_pause",
					"ok":        err == nil,
					"is_paused": *msg.Pause,
				}
				if err != nil {
					log.Printf("Error setting simulation pause: %v", err)
					controlPayload["error"] = err.Error()
				} else {
					controlPayload["is_paused"] = pauseResp.GetIsPaused()
				}

				if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
					log.Printf("Error writing control ack to websocket: %v", err)
					return
				}

			case "kill":
				killReq := &pb.KillRequest{}
				if robotID := strings.TrimSpace(msg.RobotID); robotID != "" {
					resolvedRobotID, resolveErr := resolveRobotID(robotID)
					if resolveErr != nil {
						controlPayload := map[string]interface{}{
							"type":  "kill",
							"ok":    false,
							"error": resolveErr.Error(),
						}
						if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
							log.Printf("Error writing control ack to websocket: %v", err)
							return
						}
						continue
					}
					killReq.RobotId = resolvedRobotID
				}

				killResp, err := client.Kill(context.Background(), killReq)
				controlPayload := map[string]interface{}{
					"type": "kill",
					"ok":   err == nil && killResp != nil && killResp.GetSuccess(),
				}
				if err != nil {
					log.Printf("Error killing robot: %v", err)
					controlPayload["error"] = err.Error()
				} else if killResp != nil && !killResp.GetSuccess() {
					controlPayload["error"] = killResp.GetError()
				} else if killResp != nil {
					controlPayload["killed_robot_id"] = killResp.GetKilledRobotId()
				}

				if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
					log.Printf("Error writing control ack to websocket: %v", err)
					return
				}

			case "set_partition":
				if len(msg.Assignments) == 0 {
					controlPayload := map[string]interface{}{
						"type":  "set_partition",
						"ok":    false,
						"error": "no partition assignments provided",
					}
					if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
						log.Printf("Error writing control ack to websocket: %v", err)
						return
					}
					continue
				}

				assignments := make([]*pb.PartitionAssignment, 0, len(msg.Assignments))
				for _, assignment := range msg.Assignments {
					resolvedRobotID, resolveErr := resolveRobotID(strings.TrimSpace(assignment.RobotID))
					if resolveErr != nil {
						controlPayload := map[string]interface{}{
							"type":  "set_partition",
							"ok":    false,
							"error": resolveErr.Error(),
						}
						if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
							log.Printf("Error writing control ack to websocket: %v", err)
							return
						}
						assignments = nil
						break
					}

					assignments = append(assignments, &pb.PartitionAssignment{
						RobotId:    resolvedRobotID,
						GroupIndex: assignment.GroupIndex,
					})
				}
				if assignments == nil {
					continue
				}

				partitionResp, err := client.SetNetworkPartition(context.Background(), &pb.NetworkPartitionRequest{Assignments: assignments})
				controlPayload := map[string]interface{}{
					"type": "set_partition",
					"ok":   err == nil && partitionResp != nil && partitionResp.GetSuccess(),
				}
				if err != nil {
					log.Printf("Error applying network partition: %v", err)
					controlPayload["error"] = err.Error()
				} else if partitionResp != nil && !partitionResp.GetSuccess() {
					controlPayload["error"] = partitionResp.GetError()
				} else if partitionResp != nil {
					controlPayload["applied_count"] = partitionResp.GetAppliedCount()
				}

				if err := ws.WriteJSON(map[string]interface{}{"control": controlPayload}); err != nil {
					log.Printf("Error writing control ack to websocket: %v", err)
					return
				}

			default:
				continue
			}
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
			latestRobotIDs = latestRobotIDs[:0]
			for _, robot := range robResp.GetRobots() {
				latestRobotIDs = append(latestRobotIDs, robot.GetId())
			}

			leaderLogResp, err := client.GetLeaderLog(context.Background(), &pb.LeaderLogRequest{})
			if err != nil {
				log.Printf("Error fetching leader log: %v", err)
				leaderLogResp = &pb.LeaderLogResponse{}
			}
			leaderLogPayload := toWSLeaderLogData(leaderLogResp)

			// Combine and send to WebSocket client
			payload := map[string]interface{}{
				"environment": envResp,
				"robots":      robResp.GetRobots(),
				"leader_log":  leaderLogPayload,
			}

			if err := ws.WriteJSON(payload); err != nil {
				log.Printf("Error writing to websocket: %v", err)
				return // break out if client disconnected
			}
		}
	}
}
