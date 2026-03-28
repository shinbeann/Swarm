# Objective of the Project

This project builds a 2d simulation of a robot swarm performing a search and rescue mission in a disaster area. The robots are meant to communicate information with each other, map out the area and find survivors.  

The purpose is to implement distributed computing concepts between the robots, with the rest of the simulator being a way to visualize the swarm and its behaviour as a result of the distributed computing concepts. The intention is to make the rest of the simulator as simple as possible, so that the focus is on the distributed computing concepts.  

The project is split into the following major subprojects:  
* Communications: defines the gRPC service and messages used for communications between robots, world and the visualiser.
* Robot: defines the robot and its behaviour.  
* World: serves as the central authority for the simulation.  
* Visualiser: receives data from the world and visualizes them. It uses a Backend-for-Frontend (BFF) architecture, with a Go proxy translating gRPC streams to WebSockets for a web-based React/PixiJS frontend.

# Architecture Idea

[View Detailed Codebase & Dependency Map](CODEBASE_MAP.md)

The robots and the world engine are a hub and spoke model. The world serves as the source of truth for all physics, sensor and network data of the robots.  

As part of the control loop, robots send a movement request specifying an absolute target position `(X, Y)` and a desired final heading. The WorldEngine steps each robot toward that target over time at a fixed speed (50 units/sec), handles wall collisions, and returns the robot's canonical position in every `HeartbeatResponse`. The robot reads its own position exclusively from these responses — it holds no independent physics of its own.  

The sensors of the robot are "faked", in the sense that we will not be using any emulation of real sensor behaviour. Instead, the world will provide the robots with the data that they would receive from their sensors. E.g. if the robot requests for sensor data, the world engine finds all the objects that are within the robot's sensor radius and returns them.  

The world engine also simulates the wireless communication network that will exist between the robots. This includes defining the network power, bandwidth and latency between robots, simulating real world conditions as robots move around. This is despite the robots actually having excellent network conditions in the simulation.  

Each robot launches as a docker container based on the same robot image. The world engine is a separate docker container, and the visualiser is a separate docker container, based on their respective images.  
All containers should be on the same docker network.  

A robot should only interact with another robot and the world engine. The visualiser should only interact with the world engine.  

# Subprojects Specifications

## Communications
This subproject should have the .proto files for the gRPC service and messages.  

Specifically, there should be the following services:
* RobotService: used for communication between world and robot, with the world as the server and the robots as the clients.  
    * MoveToPosition: robot submits an absolute target position `(target_x, target_y)` plus a `desired_heading` (the orientation it wants when it arrives). The WorldEngine queues this as the robot's active movement goal.  
    * GetSensorData: robot requests for sensor data from the world.  
    * GetNetworkData: robot requests for network data from the world.  
    * SendHeartbeat: robot sends a heartbeat to the world to indicate that it is still alive.  
* VisualiserService: used for communication between world and visualiser, with the world as the server and the visualiser as the client.  
    * GetEnvironmentData: visualiser requests for environment data from the world.  
    * GetRobotData: visualiser requests for robot data relevant to the visualiser from the world.
* PeerService: used for peer-to-peer communication directly between robots.
    * SyncData: robot sends a synchronization payload to a neighboring robot, which is subjected to the simulated network conditions.
    * RouteMessage: robot forwards a serialized Raft RPC (or other control-plane message) toward a destination robot via multi-hop mesh routing.
* RaftService: used for robot-to-robot consensus communication directly between robots on a dedicated service.
    * RequestVote: candidate robots request votes during election.
    * AppendEntries: leaders replicate log entries and send heartbeat-style consistency checks.

### Environment definitions
The environment defines a coordinate space starting from 0,0, up to a defined boundary.
* Obstacles: Static rectangular areas defined by an X, Y, Width, and Height that robots cannot enter. They are returned by the World Engine in `GetEnvironmentData` for the Visualiser and dynamically within range during `GetSensorData` for the Robots.

## Robot

Each robot is an independent agent that runs its own control loop, encapsulated in a `Robot` struct. It is responsible for its own movement, sensor data processing, and decision-making.  

The robot is implemented in Go. Each robot runs in its own Docker container, with the whole swarm being deployed via Docker Compose.  

Robots communicate with the World Engine using gRPC and maintain a local `LamportClock` for logical timing.

### Peer-to-Peer Communication
Robots communicate with each other natively using a localized `PeerService` hosted on a secondary port (`:50052`). This communication physically enacts the simulated network constraints bounded by the World Engine. 
In the control loop, robots evaluate their peers based on `GetNetworkData`, and attempt to propagate payloads (e.g., 1MB mock data) and synchronize their `LamportClock`:
* **Reliability:** The packet is subjected to a random chance of dropping completely before it leaves the sender.
* **Bandwidth & Latency:** If not dropped, the theoretical transmission time matching the bandwidth is calculated and added to the network latency, before artificially sleeping the goroutine to delay the actual gRPC request execution.

### Raft Consensus Communication
Robots also host a dedicated `RaftService` on port `:50053` for consensus messages (`RequestVote`, `AppendEntries`).  
Raft traffic is now routed through a **decentralized mesh routing layer** so that leaders and candidates can reach *all* peers in the swarm, not just physically adjacent ones. Each robot maintains a `RoutingTable` built via distance-vector (Bellman-Ford) route advertisements exchanged during gossip. Raft RPCs are serialized, wrapped in a `RoutedMessageRequest` envelope with a TTL, and forwarded hop-by-hop through `PeerService.RouteMessage` on `:50052`. Per-hop network constraints (latency, bandwidth, reliability) are applied at each intermediate robot.
State transitions (Follower, Candidate, Leader) are governed by a 250ms sync tick, an 8-13s jittered election timeout (increased from 4-7s to accommodate multi-hop latency), and a 10s leader ping interval to maintain consensus.

### Raft and Network Testing
To ensure the robustness of the Raft implementation and the multi-hop routing layer without introducing test flakiness from temporal conditions, a comprehensive test suite (`Robot/raft_*_test.go`, `Robot/network_test.go`) directly drives the state machine APIs (e.g., `HandleRequestVote`, `HandleAppendEntries`, `HandleRouteMessage`). It bypasses tickers, simulates network constraints and partitions by artificially manipulating `lastLeaderSeenAt`, and dynamically forces deterministic splits and routing converge scenarios. Test design principles are documented in `Robot/raft_tests/README.md`.

## World
The world subproject serves as the central authority for the simulation. It should be written in Go.  

For the purpose of this simulation, the world is a 2D plane with a defined boundary of 1000×1000 units.  

### Simulated Network 
The World Engine tracks all active robots and establishes peer availability and metrics based strictly on Euclidean distances between them when responding to `GetNetworkData`. It utilizes the following degradation parameters:
* **Max Range:** `250.0` units. Robots beyond this distance are completely invisible to the requesting peer.
* **Bandwidth:** Decays linearly. Starts at `50.0 Mbps` at distance zero, deteriorating to `1.0 Mbps` precisely at Max Range.
* **Latency:** Ramps linearly. Starts reasonably at `5.0 ms` at distance zero, dragging out to `250.0 ms` at Max Range.
* **Reliability:** Drops linearly. Assumed `1.0` (100% reliable) near distance zero, fading precisely to `0.5` (50% physical packet loss rate) at Max Range.

## Visualiser

The visualiser subproject receives data from the world and visualizes them.

It uses a **Backend-for-Frontend (BFF) architecture**: a Go proxy server bridges `VisualiserService` gRPC calls from the World Engine into WebSocket streams for a **React/PixiJS** web frontend. The Go proxy serves the pre-built static assets and handles WebSocket connections on port `8080`. The web UI is exposed at `localhost:3000` via Docker Compose.

It should be written to be viewable in a web browser. Choose simple representations for the robots, environment and other objects.

# Architecture Decision Records

Major technical and design decisions are tracked in `docs/adr/`:

| # | Title | Summary |
| :--- | :--- | :--- |
| [001](adr/001-visualiser-bff-websocket.md) | Visualiser BFF + WebSocket | The Visualiser uses a Go proxy to bridge gRPC to WebSockets for the browser UI. |
| [002](adr/002-go-grpc-language-stack.md) | Go + gRPC Language Stack | Robot and WorldEngine are implemented in Go with protobuf-generated gRPC stubs. |
| [003](adr/003-world-engine-obstacle-authority.md) | World Engine Obstacle Authority | The World Engine is the sole source of obstacle data; robots receive proximity-filtered sensor data only. |
| [004](adr/004-peer-to-peer-network-simulation.md) | Peer-to-Peer Network Simulation | Robots host a `PeerService` gRPC server directly, while the World Engine acts as a network oracle supplying distance-based bandwidth, latency, and reliability metrics. |
| [005](adr/005-dedicated-raft-service-with-shared-network-constraints.md) | Dedicated Raft Service with Shared Network Constraints | Robots keep main gossip architecture while adding dedicated constrained Raft traffic over `RaftService` with no status endpoint. |
| [006](adr/006-decentralized-mesh-routing.md) | Decentralized Mesh Routing | Distance-vector routing layer enabling multi-hop Raft communication across the entire swarm via `PeerService.RouteMessage`. |

# Current Implementation State

The following features are implemented and running in the current Docker Compose cluster:

- **Robot control loop**: Runs on two independent tickers:
  - **30 ms heartbeat ticker (~33 Hz)**: calls `SendHeartbeat` (receives canonical position back), `GetSensorData`, and — every 2 s — triggers the P2P sync. The robot's local `X/Y/Heading` are updated exclusively from the `HeartbeatResponse`; the robot performs no local physics.
  - **2 s movement ticker**: calls `MoveToPosition` with an absolute target position and desired final heading. Target is chosen randomly 100–300 units ahead; if an obstacle was recently sensed within 5 units, the target is biased π radians away.
- **WorldEngine physics authority**: The WorldEngine owns all robot positions. On each 30 ms simulation tick it steps every robot toward its active target at 50 units/sec (~1.5 units/step). On wall contact the robot halts and its target is cleared. `SendHeartbeat` only seeds a robot's initial position on first contact; all subsequent heartbeats only refresh `LastSeen`.
- **Boundary walls**: The World Engine spawns four rectangular walls at the edges of the 1000×1000 world at startup.
- **Visualiser dashboard**: The React+PixiJS UI renders environment boundary, obstacles (red fill), and robots (green triangles) in real-time via WebSocket.
- **Simulated network constraints**: The World Engine's `GetNetworkData` calculates per-robot peer conditions based on Euclidean distance. Max communication range is `250.0` units with linear degradation of bandwidth (50→1 Mbps), latency (5→250 ms), and reliability (1.0→0.5).
- **Robot P2P sync**: Each robot hosts a `PeerService` gRPC server on port `:50052`. Every 2 seconds, it fetches peer conditions from the World Engine and dispatches a `SyncData` call to each in-range neighbor, subject to simulated packet drops (reliability), and `time.Sleep`-based delay (latency + bandwidth transfer time for a 1MB payload). The sync now includes a `LamportClock` to maintain logical time across the swarm.
- **Mesh routing layer**: Each robot maintains a `RoutingTable` (distance-vector) that is advertised via gossip. Routes have a 10s expiry. `PeerService.RouteMessage` wraps serialized Raft RPCs in a `RoutedMessageRequest` envelope with TTL and forwards them hop-by-hop until they reach the destination.
- **Robot Raft service**: Each robot hosts a dedicated `RaftService` gRPC server on port `:50053` and runs a 250 ms Raft tick. Raft now iterates all reachable peers from the routing table (not just direct neighbours) and sends traffic via the mesh router. Election timeout is 8-13s to accommodate multi-hop latency.
