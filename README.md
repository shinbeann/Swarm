# Swarm Project

A 2D simulation of a robot swarm performing distributed search and rescue missions in a disaster area.

This project explores distributed computing concepts. The robots form a swarm to discover landmarks, gossip knowledge to peers, and run a consensus layer for leader election. The simulator visualizes the swarm's behavior resulting from these distributed algorithms.

## Project Architecture

The architecture relies on a **Hub and Spoke** pattern for simulation physics, while preserving distributed concepts for the robots' logic:
* **World Engine (Go/gRPC)**: The central authority. It simulates the 2D plane, places landmarks, generates boundary obstacles, processes robot intents (movement), and provides restricted, "faked" localized sensor data and simulated network constraint conditions to individual robots based on a max communication range of 250 units.
* **Robots (Go/gRPC)**: Independent agents running individual control loops. Each robot coordinates two parallel communication subsystems:
  * **Gossip subsystem (`PeerService` `:50052`)**: Discovers landmarks via sensor data, stores them in a local `KnowledgeStore`, and uses a `GossipEngine` to propagate discoveries to active neighbours using eventual-consistency style dissemination.
  * **Raft consensus subsystem (`RaftService` `:50053`)**: Runs an in-memory Raft state machine for leader election and log replication, strictly separate from gossip traffic.
* **Communications**: Protobuf definitions defining the strictly enforced gRPC contracts between the World, Robots, and Visualiser, including `PeerService` for gossip and `RaftService` for consensus.
* **Visualiser (Go/React/PixiJS)**: A high-performance web dashboard. It utilizes a **Backend-for-Frontend (BFF)** architecture where a Go proxy subscribes to the World Engine via gRPC and streams environment limits, obstacles, landmarks, and robot telemetry to a Vite+React web UI over WebSockets.

For an architecture deep dive (module map, dependencies, timers, and ADR index), see [docs/CODEBASE_MAP.md](docs/CODEBASE_MAP.md).

## Prerequisites

* Docker
* Docker Compose

## Getting Started

The entire swarm simulation is containerized and orchestrated via Docker Compose.

1. **Clone the repository.**
2. **Build and start the simulation:**
   ```bash
   docker compose up --build
   ```
   This will spin up:
   - 1 `world-engine` container on port `50051`.
   - 5 `robot` instances (scalable via `docker-compose.yml`) navigating the simulation.
   - 1 `visualiser` proxy container on port `8080`, exposing the web UI on port `3000`.

3. **View the Simulation:**
   Open a web browser and navigate to:
   ```
   http://localhost:3000
   ```
   You will see the live swarm dashboard charting the boundaries, obstacles, landmarks, and tracked robots.

## Expanding the Swarm

You can start any number of robot containers without manually duplicating services.

Example: start 10 robots:

```bash
docker compose up --build --scale robot=10
```

Each robot will derive a unique ID from its container hostname when no `ROBOT_ID`/`-id` is provided, so the visualiser and world engine will receive distinct robot identifiers automatically.

## Robot Status Endpoint

Each robot exposes a local HTTP status endpoint for runtime observability of both the gossip and Raft layers:

- `GET /status` (default bind address `:8081`, configurable via `-status-addr`)

Example query from inside the Docker network:

```bash
curl http://robot:8081/status
```

Response fields include:
- **Raft**: `raft_state`, `raft_term`, `known_leader_id`, `log_length`, `commit_index`, timing metadata
- **Gossip**: `landmark_count`, `active_neighbours`
