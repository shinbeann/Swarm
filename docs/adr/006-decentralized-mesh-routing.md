# Decentralized Mesh Routing for Multi-Hop Raft Communication

## Context

The Raft implementation (ADR-005) relied on `activeNeighbours()` — peers returned by the WorldEngine's `GetNetworkData`, limited to a 250-unit Euclidean range. This meant Raft leaders/candidates could only communicate with physically adjacent robots, violating the Raft requirement that the leader must reach *all* followers in the cluster.

## Options Considered

### Option A: Centralized Routing Oracle (World Engine)

The WorldEngine already has omniscient knowledge of every robot's position. It could run Dijkstra's algorithm across the full swarm topology on every `GetNetworkData` call, returning a pre-computed next-hop route to every peer alongside the existing bandwidth/latency/reliability fields. Robots would simply read their routing table from the World Engine the same way they already consume network conditions.

**Why we rejected this:**

- **It defeats the purpose of the simulation.** The core objective of this project is to implement and observe distributed computing concepts inside the swarm. If the WorldEngine hands each robot a perfect, globally-computed routing table, the robots are not doing any distributed coordination — they are just executing a centrally-computed plan. There would be nothing interesting to observe about multi-hop consensus.
- **It is not physically realistic.** Real ad-hoc mesh networks (such as those used by actual robot swarms) have no central coordinator. Routes must be discovered by the nodes themselves from local information alone.
- **Network partition behaviour would be artificial.** With a central oracle, a robot always "knows" the full topology even when it is physically isolated. The oracle would correctly refuse to supply routes across a partition, but this requires the oracle itself to encode partition logic — something that belongs in the swarm.
- **It tightly couples routing to the WorldEngine.** Adding or changing any routing policy would require changes to the simulation authority rather than to the robots themselves, mixing concerns.

### Option B: Decentralized Distance-Vector Routing over Gossip (Chosen)

Each robot builds its own routing table from information exchanged with its 1-hop physical neighbours. Route advertisements piggyback on the existing gossip protocol. No robot requires knowledge of the global topology; each only knows what its neighbours have told it.

**Why we chose this:**

- **Realistic emergent behaviour.** Routes converge gradually as gossip propagates through the swarm, creating observable network warm-up, convergence delays, and realistic partition detection.
- **True decentralization.** No single point of failure or coordination authority. The swarm discovers its own connectivity through purely local interactions.
- **Consistent with the project's distributed computing educational goals.** Students and observers can watch route tables converge, see Raft elections stall during network partitions, and observe leader election correctly stabilise in the majority partition only after convergence.
- **Composable with existing architecture.** Route advertisements are carried as a new field in `GossipMessage`, reusing the existing gossip infrastructure without adding a new communication channel.

## Decision

Introduce a **decentralized distance-vector mesh routing layer** that enables multi-hop communication across the entire swarm:

1. **Routing Table (`routing_table.go`)**: Each robot maintains a `RoutingTable` mapping every known peer to a `Route{NextHop, HopCount, LastUpdated}`. Routes expire after 10 seconds.

2. **Route Discovery via Gossip**: The `GossipMessage` struct now includes a `Routes map[string]int` field. On each gossip tick, the robot advertises its routing table to a random 1-hop neighbour. On receive, a Bellman-Ford merge updates the local table: if sender reaches destination X in N hops, we can reach X via sender in N+1 hops.

3. **`RouteMessage` RPC**: A new RPC on `PeerService` wraps serialized Raft requests in a `RoutedMessageRequest{SourceId, DestinationId, MessageType, Payload, TTL}` envelope. Intermediate robots decrement TTL and forward to the next hop. The destination robot deserializes and dispatches the inner Raft RPC locally.

4. **Raft uses mesh routing**: `syncRaftWithPeers` now iterates `routingTable.GetAllReachable()` (all multi-hop peers) instead of `activeNeighbours()` (1-hop only). Raft RPCs are serialized with `proto.Marshal`, sent via `meshRouter.SendMessage`, and deserialized on response. Direct gRPC dials to `:50053` are replaced entirely.

5. **Adjusted timeouts**: Election timeout increased from 4-7s to 8-13s to account for multi-hop RTT.

## Consequences

Benefits:

- Raft leaders can now reach all followers regardless of physical distance, maintaining correct consensus semantics.
- Route discovery is fully decentralized — no WorldEngine involvement in routing decisions.
- Network partitions emerge naturally when gossip cannot bridge two groups.

Trade-offs:

- Multi-hop RPCs incur cumulative per-hop latency and compounding drop probability.
- Larger Raft timeouts reduce failover responsiveness.
- Routing convergence takes multiple gossip cycles proportional to network diameter.

Supersedes: This ADR extends ADR-005. The `RaftService` on `:50053` still exists for backward compatibility but is no longer the primary Raft transport; all Raft traffic is now tunneled through `PeerService.RouteMessage` on `:50052`.
