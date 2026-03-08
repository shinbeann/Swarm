This project builds a 2d simulation of a robot swarm performing a search and rescue mission in a disaster area. The robots are meant to communicate information with each other, map out the area and find survivors.  

The purpose is to implement distributed computing concepts between the robots, with the rest of the simulator being a way to visualize the swarm and its behaviour as a result of the distributed computing concepts. The intention is to make the rest of the simulator as simple as possible, so that the focus is on the distributed computing concepts.  

The project is split into the following major subprojects:  
* Communications: defines the gRPC service and messages used for communications between robots, world and the visualiser.
* Robot: defines the robot and its behaviour.  
* World: serves as the central authority for the simulation.  
* Visualiser: receives data from the world and visualizes them. It uses a Backend-for-Frontend (BFF) architecture, with a Go proxy translating gRPC streams to WebSockets for a web-based React/PixiJS frontend.

# Architecture

[View Detailed Codebase & Dependency Map](CODEBASE_MAP.md)

The robots and the world engine are a hub and spoke model. The world serves as the source of truth for all physics, sensor and network data of the robots.  

As part of the control loop, robots will send a request for movement based on a position or director vector with velocity. The world will then update the robot's position and heading based on this request.  

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
    * MoveToPosition: robot signifies an intention to move to a position or along a direction vector with a velocity.  
    * GetSensorData: robot requests for sensor data from the world.  
    * GetNetworkData: robot requests for network data from the world.  
    * SendHeartbeat: robot sends a heartbeat to the world to indicate that it is still alive.  
* VisualiserService: used for communication between world and visualiser, with the world as the server and the visualiser as the client.  
    * GetEnvironmentData: visualiser requests for environment data from the world.  
    * GetRobotData: visualiser requests for robot data relevant to the visualiser from the world.
* PeerService: used for peer-to-peer communication directly between robots.
    * SyncData: robot sends a synchronization payload to a neighboring robot, which is subjected to the simulated network conditions.

Note that the grpc implementations of these messages and services should be put into the respective subprojects.  

### Environment definitions
The environment defines a coordinate space starting from 0,0, up to a defined boundary.
* Obstacles: Static rectangular areas defined by an X, Y, Width, and Height that robots cannot enter. They are returned by the World Engine in `GetEnvironmentData` for the Visualiser and dynamically within range during `GetSensorData` for the Robots.

## Robot

Each robot is an independent agent that runs its own control loop. It is responsible for its own movement, sensor data processing and decision making.  

The robot should be defined in Go. Each robot will run in its own docker container, with the whole swarm being deployed via docker compose.  

Robots also communicate with the world engine using gRPC.  

### Peer-to-Peer Communication
Robots will communicate with each other natively using a localized `PeerService` hosted on a secondary port (`:50052`). This communication physically enacts the simulated network constraints bounded by the World Engine. 
In the control loop, robots evaluate their peers based on `GetNetworkData`, and attempt to propagate payloads (e.g. 1MB mock data):
* **Reliability:** The packet is subjected to a random chance of dropping completely before it leaves the sender.
* **Bandwidth & Latency:** If not dropped, the theoretical transmission time matching the bandwidth is calculated and added to the network latency, before artificially sleeping the goroutine to delay the actual gRPC request execution.

## World
The world subproject serves as the central authority for the simulation. It should be written in Go.  

For the purpose of this simulation, the world is a 2d plane with a defined boundary.  

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

# Current Implementation State

The following features are implemented and running in the current Docker Compose cluster:

- **Robot control loop**: Runs at **30ms intervals (~33 Hz)**. Each tick calls `SendHeartbeat`, `GetSensorData`, and `MoveToPosition` against the World Engine. Note: the velocity applied to movement uses a hardcoded `0.2s` time delta (equivalent to 5 FPS), which does not match the actual 33 Hz tick rate — this is a known inconsistency to be resolved when implementing proper kinematics. The World Engine tracks robots in a registry, pruning any that miss heartbeats for 5+ seconds (checked every 2s by a cleanup ticker).
- **Random movement with avoidance**: Robots drift randomly with a slight heading bias. If `GetSensorData` returns an obstacle within 5 units, the robot reverses its heading by π radians.
- **Boundary walls**: The World Engine spawns four rectangular walls at the edges of the 1000×1000 world at startup.
- **Visualiser dashboard**: The React+PixiJS UI renders environment boundary, obstacles (red fill), and robots (green triangles) in real-time via WebSocket.
- **Simulated network constraints**: The World Engine's `GetNetworkData` calculates per-robot peer conditions based on Euclidean distance. Max communication range is `250.0` units with linear degradation of bandwidth (50→1 Mbps), latency (5→250 ms), and reliability (1.0→0.5).
- **Robot P2P sync**: Each robot hosts a `PeerService` gRPC server on port `:50052`. Every 2 seconds, it fetches peer conditions from the World Engine and dispatches a `SyncData` call to each in-range neighbor, subject to simulated packet drops (reliability), and `time.Sleep`-based delay (latency + bandwidth transfer time for a 1MB payload).
