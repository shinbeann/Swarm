# Gossip-Driven Landmark State Propagation and Local Knowledge Store

## Context

Robots discover landmarks from the WorldEngine's sensor feed and exchange those discoveries with neighbouring robots over the existing peer-to-peer gossip channel. The implementation in the robot package combines two concerns:

1. spreading landmark knowledge across the swarm, and
2. keeping a local store that can decide when a casualty report has enough independent reports to be considered verified.

The implementation is intentionally simple, but it is easy to misread it as a full event-driven forwarding system. It is not that.

## Decision

We treat gossip as a periodic anti-entropy exchange rather than an immediate relay mechanism.

### What the gossip engine does

1. Every second, the robot selects one eligible active neighbour in a deterministic round-robin order and sends a single gossip message to that peer.
2. Before sending, it prunes expired routes from the local routing table.
3. The outgoing gossip message carries a full snapshot of the robot's current landmark knowledge plus a routing advertisement.
4. When a landmark is first seen by the robot through the WorldEngine sensor feed, the robot records that discovery locally and includes it in future gossip payloads.

### What the gossip engine does not do

1. It does not forward a received gossip message immediately to other peers.
2. It does not rebroadcast every incoming report.
3. It does not generate landmark IDs itself.
4. It does not reconcile conflicting landmark metadata by location or type.
5. It does not delete or rewrite entries when a peer reports the same landmark again.

### Gossip message content

The in-memory gossip payload is `GossipMessage`, which contains:

1. `SenderID`: the sending robot ID.
2. `Timestamp`: the sender's Lamport clock value at send time.
3. `Entries`: the sender's full list of known landmark entries.
4. `Routes`: a routing advertisement of destination ID to hop count.

On the wire, the robot wraps that JSON payload in `PeerSyncRequest`, which also includes `sender_id` and `lamport_clock`.

### What happens on receive

When a robot receives peer state:

1. The peer service accepts the `SyncData` RPC and passes the request to the robot.
2. The robot updates its Lamport clock from the transport metadata.
3. The robot marks the sender as an active neighbour.
4. The JSON payload is decoded into a `GossipMessage`.
5. `OnReceive` updates the Lamport clock again using the message timestamp.
6. Each incoming entry in the payload is merged into the knowledge store, preserving the embedded eyewitness `Reporters` map from the sender's local state.
7. The sender is recorded as a direct 1-hop route.
8. The sender's routing advertisement is merged into the routing table using distance-vector logic.

This is a receive-and-merge path only. It is not a store-and-forward path.

### How landmark matching works in the knowledge store

The knowledge store uses `LandmarkID` as the primary key.

1. If an incoming report has an ID that is not already in the map, a new `LandmarkEntry` is created.
2. If the ID already exists, the report is treated as evidence for the existing landmark.
3. The store merges the incoming entry's `Reporters` map into the local entry. The sender itself is not automatically counted as a reporter unless it actually appears in the embedded `Reporters` map.
4. For casualty landmarks, once the number of distinct reporters reaches `VerificationQuorum` (3), the entry is marked `Verified`.

Matching is therefore by ID only, not by coordinates, type, or fuzzy spatial comparison.

### Origin of landmark IDs

The robot does not invent landmark IDs.

The WorldEngine assigns stable IDs when it spawns landmarks at startup, for example `casualty-0`, `casualty-1`, and `corridor-0`. When a robot senses a landmark, it reads `ObjectData.id` from the sensor response and uses that value as the `LandmarkID` key in its local store.

## Consequences

Benefits:

1. Landmark state converges gradually across the swarm without requiring a central coordinator.
2. Casualty verification becomes a local quorum problem rather than a global service.
3. The behaviour is easy to reason about: periodic send, local merge, ID-based deduplication.

Trade-offs:

1. Because the store keys only by `LandmarkID`, conflicting sensor reports for the same ID are not automatically resolved.
2. If the WorldEngine ever emitted duplicate IDs for distinct objects, the store would merge them incorrectly.
3. Since receive-time forwarding is absent, convergence depends on the periodic gossip loop rather than immediate propagation.
4. Gossip selection is now deterministic, but the eligible peer set can still change between ticks; peers only re-enter the cycle when they become eligible again.

## Notes

This design fits the current codebase, but it reflects a minimal interpretation of distributed landmark sharing rather than a more aggressive epidemic protocol. If stronger consistency or conflict resolution is needed later, the store will need explicit merge rules beyond simple ID-based lookup.