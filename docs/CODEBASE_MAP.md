# Codebase Architecture Map

This document serves as the high-level overview of the `SwarmProject` directory structure, core module responsibilities, and internal Go package dependencies.

## High-Level Directory Tree

*   **`Communications/`**: Contains the protocol buffer (`.proto`) definitions for gRPC communications between the robots, world, and visualiser.
*   **`Robot/`**: Defines behavior, control loops, movement logic, constrained peer gossip (`PeerService`), and constrained dedicated consensus traffic (`RaftService`) for each robot.
*   **`WorldEngine/`**: Serves as the central server and source of truth for the simulation, providing faked sensor data and enforcing simulated network boundaries (bandwidth, latency, reliability) between robots.
*   **`Visualiser/`**: A web-based visualizer featuring a React/PixiJS frontend in `/ui` and a Backend-for-Frontend (BFF) proxy connecting gRPC to WebSockets in `/server`.
*   **`docs/`**: Contains the project's living knowledge base (`PROJECT_KNOWLEDGE.md`), architecture maps, and Architecture Decision Records (ADRs).
*   **`.agents/`**: Contains the system workflows, rules (`project-rules.md`), and skills utilized by the AI agent to maintain the project.

## Core Modules & Responsibilities

| Module | Primary Responsibility |
| :--- | :--- |
| **Communications** | API contracts. Defines standard gRPC services (`RobotService`, `VisualiserService`, `PeerService`, `RaftService`). |
| **Robot** | Agent-level decision-making. Consumes data from the world, performs constrained peer gossip, and runs constrained Raft election/replication over a dedicated service. |
| **WorldEngine** | Simulation arbitration. Owns all robot positions, advances robots toward their requested targets every 30 ms, validates wall collisions, mocks object proximities, and calculates communication decay metrics based on Euclidean distance. |
| **Visualiser** | Observation. Subscribes to environment and robot data from the World Engine without interfering in simulation physics. |

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
| **Robot** | P2P network sync | **Every 2 s** | `GetNetworkData` → goroutine per peer dispatching simulated `SyncData` (including `LamportClock`). |
| **Robot** | Raft sync tick | **Every 250 ms** | Uses peers discovered from main gossip neighbor tracking (fed by WorldEngine `GetNetworkData` heartbeat tick) to dispatch constrained `RequestVote`/`AppendEntries` over `RaftService`. |
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
