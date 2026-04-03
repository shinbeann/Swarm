# Dedicated Raft Service with Shared Network Constraints

## Context

The project previously evolved two competing directions:

- Keep the main branch gossip architecture (`PeerService`, gossip engine, world-driven network discovery, routing-table-driven neighbor reachability).
- Introduce leader-feat branch consensus behavior (Raft election and replication).

The merge decision requires preserving the main peer architecture while introducing Raft in a way that does not fork peer-discovery responsibilities or duplicate network-condition sourcing logic.

## Decision

Robots run two inter-robot services:

- `PeerService` on `:50052` for gossip payload sync and Lamport clock exchange.
- `RaftService` on `:50053` for consensus traffic (`RequestVote`, `AppendEntries`).

Raft peer targeting uses the same world-derived neighbor discovery path as gossip:

- WorldEngine `GetNetworkData` is refreshed in the heartbeat-driven world tick.
- Active-neighbor selection is derived from current world link conditions and routing-table state (no separate neighbor registry).

Network constraint simulation is shared across both traffic classes:

- Reliability-based packet drop.
- Latency + bandwidth delay before RPC dispatch.
- Delay calculations are based on actual serialized payload size at send time.

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

1.  **Raft Sync Tick Interval (`500ms`)**: The primary cadence for Raft coordination. Every 500ms, the robot checks if its election timeout has expired (potentially starting a new election) and synchronizes with known peers (sending `RequestVote` if Candidate, or `AppendEntries` if Leader).
2.  **Election Timeout (`10s - 17s`)**: Followers expect to hear from the Leader within this window. It is composed of a minimum timeout of 10 seconds plus a random jitter of up to 7 seconds. If the timeout expires without communication from a valid Leader, the Follower converts to a Candidate and requests votes.
3.  **Leader Ping Interval (`10s`)**: To maintain authority in absence of actual append payloads, the Leader automatically generates a lightweight `leader_ping` log entry to replicate to followers if 10 seconds elapse without any other outbound events. This ensures followers' election timers are consistently reset.
4.  **Candidate Vote Retry (`2s`)**: While in Candidate state, outbound vote requests are retried at most once every 2 seconds.
5.  **Data Replication Constraints**: Both `RequestVote` and `AppendEntries` RPC calls are subject to the same simulated network restrictions (latency, packet drop based on reliability, and simulated bandwidth limits based on actual serialized payload size) as the primary gossip engine.