# Dedicated Raft Service with Shared Network Constraints

## Context

The project previously evolved two competing directions:

- Keep the main branch gossip architecture (`PeerService`, gossip engine, neighbor registry, world-driven network discovery).
- Introduce leader-feat branch consensus behavior (Raft election and replication).

The merge decision requires preserving the main peer architecture while introducing Raft in a way that does not fork peer-discovery responsibilities or duplicate network-condition sourcing logic.

## Decision

Robots run two inter-robot services:

- `PeerService` on `:50052` for gossip payload sync and Lamport clock exchange.
- `RaftService` on `:50053` for consensus traffic (`RequestVote`, `AppendEntries`).

Raft peer targeting uses the same world-derived neighbor discovery path as gossip:

- WorldEngine `GetNetworkData` is refreshed in the heartbeat-driven world tick.
- Main neighbor tracking/active-neighbor logic remains the common source for which peers are considered reachable.

Network constraint simulation is shared across both traffic classes:

- Reliability-based packet drop.
- Latency + bandwidth delay before RPC dispatch.
- Existing payload-size assumptions remain unchanged (`gossipPayloadMb = 1.0`, `raftPayloadMb = 0.001`).

No HTTP status endpoint is included in this architecture.

## Consequences

Benefits:

- Preserves the existing main branch gossip architecture and discovery ownership.
- Introduces dedicated Raft transport separation without coupling consensus to gossip payload handlers.
- Keeps one conceptual network model for both gossip and Raft channels.

Trade-offs:

- Additional robot listener surface (`:50053`) and more concurrent RPC activity.
- Consensus observability depends on logs/tests rather than an explicit runtime status endpoint.

Operational implications:

- Continual background network constraint enforcement applies shared packet drop, latency, and bandwidth limits to Raft logic.

### Raft Algorithm and Timers

The implemented Raft algorithm operates using a push-based model tied directly to the robot's main event loops. State transitions (Follower, Candidate, Leader) and data replication are governed by a specific set of timers to maintain consensus:

1.  **Raft Sync Tick Interval (`250ms`)**: The primary cadence for Raft coordination. Every 250ms, the robot checks if its election timeout has expired (potentially starting a new election) and synchronizes with known peers (sending `RequestVote` if Candidate, or `AppendEntries` if Leader).
2.  **Election Timeout (`4s - 7s`)**: Followers expect to hear from the Leader within this window. It is composed of a minimum timeout of 4 seconds plus a random jitter of up to 3 seconds. The jitter prevents split votes during elections. If the timeout expires without communication from a valid Leader, the Follower converts to a Candidate and requests votes.
3.  **Leader Ping Interval (`10s`)**: To maintain authority in absence of actual append payloads, the Leader automatically generates a lightweight `leader_ping` log entry to replicate to followers if 10 seconds elapse without any other outbound events. This ensures followers' election timers are consistently reset.
4.  **Data Replication Constraints**: Both `RequestVote` and `AppendEntries` RPC calls are subject to the same simulated network restrictions (latency, packet drop based on reliability, and simulated bandwidth limits for a 1KB control plane payload) as the primary gossip engine.

- Documentation must describe both services, the shared network-constraint model, and the detailed Raft interaction loops.
