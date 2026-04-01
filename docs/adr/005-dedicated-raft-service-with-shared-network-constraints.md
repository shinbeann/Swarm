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

- Raft cadence remains at 250 ms and gossip cadence remains unchanged.
- Documentation must describe both services and the shared network-constraint model.
