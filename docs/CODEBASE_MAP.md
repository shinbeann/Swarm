# Codebase Architecture Map

This document serves as the high-level overview of the `SwarmProject` directory structure, core module responsibilities, and internal Go package dependencies.

## High-Level Directory Tree

*   **`Communications/`**: Contains the protocol buffer (`.proto`) definitions for gRPC communications between the robots, world, and visualiser.
*   **`Robot/`**: Defines behavior, control loops, movement logic, constrained peer gossip (`PeerService`), decentralized mesh routing (`routing_table.go`, `mesh_router.go`), and constrained dedicated consensus traffic (`RaftService`) for each robot.
*   **`WorldEngine/`**: Serves as the central server and source of truth for the simulation, providing faked sensor data and enforcing simulated network boundaries (bandwidth, latency, reliability) between robots.
*   **`Visualiser/`**: A web-based visualizer featuring a React/PixiJS frontend in `/ui` and a Backend-for-Frontend (BFF) proxy connecting gRPC to WebSockets in `/server`.
*   **`docs/`**: Contains the project's living knowledge base (`PROJECT_SPECIFICATION.md`), architecture maps, test design notes, and Architecture Decision Records (ADRs).
*   **`.agents/`**: Contains the system workflows, rules (`project-rules.md`), and skills utilized by the AI agent to maintain the project.

## Core Modules & Responsibilities

| Module | Primary Responsibility |
| :--- | :--- |
| **Communications** | API contracts. Defines standard gRPC services (`RobotService`, `VisualiserService`, `PeerService` with `RouteMessage`, `RaftService`). |
| **Robot** | Agent-level decision-making. Consumes data from the world, performs constrained peer gossip, maintains a decentralized distance-vector routing table, and runs Raft election/replication over the mesh routing layer. |
| **WorldEngine** | Simulation arbitration. Owns all robot positions, advances robots toward their requested targets every 30 ms, validates wall collisions, mocks object proximities, and calculates communication decay metrics based on Euclidean distance. |
| **Visualiser** | Observation. Subscribes to environment and robot data from the World Engine without interfering in simulation physics. |

## Testing Infrastructure

*   **`docs/TESTING.md`**: Documents test design principles and rationale for deterministic Raft/network tests.
*   **`Robot/raft_*_test.go`, `Robot/network_test.go`**: Due to unexported method testing requirements (testing `package main` internal logic without exposing it), the Raft and Network test suite files live alongside `robot.go`. They manually drive the push-based state machine, bypass timing flakiness by manipulating internal clocks or injecting constrained network payloads, and verify election edge cases, routing convergence, split-brain scenarios, and leader failures without spinning up actual goroutines or gRPC servers.

## Internal Dependency Map (Go)

The following Mermaid diagram illustrates the internal dependency flow within the `github.com/yihre/swarm-project` module namespace, as extracted by the agent `Analyze Codebase Structure` skill.

```mermaid
graph TD
    "robot" --> "communications/proto"
    "worldengine" --> "communications/proto"
    "visualiser/server" --> "communications/proto"
```
*(Dependencies are automatically parsed from local `go.mod` imports via the `/update-architecture` workflow)*

## Timing Frequencies

| Component | Loop / Timer | Interval | Notes |
| :--- | :--- | :--- | :--- |
| **Robot** | Heartbeat ticker | **30 ms (~33 Hz)** | Each tick: `SendHeartbeat` (reads canonical X/Y/Heading from response) → `GetSensorData` (stores nearest obstacle direction). Robot holds no local physics. |
| **Robot** | Movement ticker | **Every 2 s** | `MoveToPosition` with absolute target `(X, Y)` + `desired_heading`. Target is chosen 100–300 units away; biased away from last sensed obstacle if one was within 5 units. |
| **Robot** | Gossip / P2P sync loop | **Every 1 s** | Selects one eligible in-range neighbour in deterministic round-robin order, sends simulated constrained `SyncData` (including `LamportClock` and route advertisement) based on latest `GetNetworkData` conditions. |
| **Robot** | Raft sync tick | **Every 500 ms** | Iterates all reachable peers from the routing table (including multi-hop). Serializes Raft RPCs, sends via `MeshRouter.SendMessage` through `PeerService.RouteMessage` with per-hop network constraints. |
| **Robot** | Raft election timeout | **10 - 17 s** | Random jittered timeout (10s base + 7s jitter). Converts Follower to Candidate if no valid Leader communication is received. |
| **Robot** | Candidate vote retry | **Every 2 s** | While in Candidate state, throttles `RequestVote` retransmission attempts to once every 2 seconds. |
| **Robot** | Raft leader ping | **Every 10 s** | Leader appends a lightweight `leader_ping` log entry to maintain authority if no other outbound appends occur. |
| **WorldEngine** | Physics simulation tick | **Every 30 ms** | Advances every robot with an active target by `50 units/sec × 0.03 s = 1.5 units`. Snaps to target on arrival; stops and clears target on wall contact. |
| **WorldEngine** | Stale-robot cleanup | **Every 2 s** | Removes robots whose `LastSeen` exceeds **5 s** from the registry. |

## Architecture Decision Records

| # | Title | Summary |
| :--- | :--- | :--- |
| [001](adr/001-visualiser-bff-websocket.md) | Visualiser BFF + WebSocket | The Visualiser uses a Go proxy to bridge gRPC to WebSockets for the browser UI. |
| [002](adr/002-go-grpc-language-stack.md) | Go + gRPC Language Stack | Robot and WorldEngine are implemented in Go with protobuf-generated gRPC stubs. |
| [003](adr/003-world-engine-obstacle-authority.md) | World Engine Obstacle Authority | The World Engine is the sole source of obstacle data; robots receive proximity-filtered sensor data only. |
| [004](adr/004-peer-to-peer-network-simulation.md) | Peer-to-Peer Network Simulation | Robots host a `PeerService` gRPC server directly, while the World Engine acts as a network oracle supplying distance-based bandwidth, latency, and reliability metrics. |
| [005](adr/005-dedicated-raft-service-with-shared-network-constraints.md) | Dedicated Raft Service with Shared Network Constraints | Robots keep main gossip architecture, add `RaftService` on a separate port, reuse world-derived peer discovery, and apply the same simulated constraints to gossip and raft traffic without a status endpoint. |
| [006](adr/006-decentralized-mesh-routing.md) | Decentralized Mesh Routing | Distance-vector routing layer enabling multi-hop Raft communication across the entire swarm via `PeerService.RouteMessage`. |
