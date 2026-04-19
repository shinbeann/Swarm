# Gossip-Driven Landmark State Propagation and Local Knowledge Store

## Context

Robots discover landmarks from the WorldEngine's sensor feed and exchange those discoveries with neighbouring robots over the existing peer-to-peer gossip channel. The implementation in the robot package combines two concerns:

1. spreading landmark knowledge across the swarm, and
2. keeping a local store that can decide when a casualty report has enough independent reports to be considered verified, and when that verified casualty has been committed through Raft.

The implementation is intentionally simple, but it is easy to misread it as a full event-driven forwarding system. It is not that.

## Decision

We treat gossip as a periodic anti-entropy exchange rather than an immediate relay mechanism.

### What the gossip engine does

1. Every second, the robot selects up to three eligible active neighbours at random and considers each target independently.
2. Before sending, it prunes expired routes from the local routing table.
3. For each target peer, it computes a delta of only the entries newer than that peer's last successful sync watermark (`lastSyncTime[peer]`).
4. The per-peer delta is sorted by priority: casualty entries first, then newer entries, then stable ID tie-break.
5. The delta is capped by the link's advertised bandwidth (`NetworkData.bandwidth`) before send.
6. The outgoing gossip message carries the selected delta plus a routing advertisement.
7. The per-peer watermark is advanced only after a successful send.
8. When a landmark is first seen by the robot through the WorldEngine sensor feed, the robot records that discovery locally and it becomes eligible for deltas to peers that have not yet synced past that timestamp.

### What the gossip engine does not do

1. It does not forward a received gossip message immediately to other peers.
2. It does not rebroadcast every incoming report.
3. It does not generate landmark IDs itself.
4. It does not reconcile conflicting landmark metadata by location or type.
5. It does not delete or rewrite entries when a peer reports the same landmark again.
6. It does not retransmit entries that have already been synced to a peer, as tracked by the per-peer lastSyncTime watermark.

### Gossip message content

The in-memory gossip payload is `GossipMessage`, which contains:

1. `SenderID`: the sending robot ID.
2. `Timestamp`: the sender's Lamport clock value at send time.
3. `Entries`: a per-target delta subset of landmark entries (not always the full store snapshot).
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

This is a receive-and-merge path only. It is not immediate message forwarding. Propagation still happens hop-by-hop because each robot periodically re-gossips its own merged store state as new deltas.

### How landmark matching works in the knowledge store

The knowledge store uses `LandmarkID` as the primary key.

1. If an incoming report has an ID that is not already in the map, a new `LandmarkEntry` is created.
2. If the ID already exists, the report is treated as evidence for the existing landmark.
3. The store merges the incoming entry's `Reporters` map into the local entry. The sender itself is not automatically counted as a reporter unless it actually appears in the embedded `Reporters` map.
4. For casualty landmarks, once the number of distinct reporters reaches `VerificationQuorum` (3), the entry is marked `Verified`. In this codebase, `Verified` means the casualty has quorum but has not yet been committed by Raft.
5. Once the verified casualty is applied from a committed Raft log entry, it becomes `Committed`.

Matching is therefore by ID only, not by coordinates, type, or fuzzy spatial comparison.

### Origin of landmark IDs

The robot does not invent landmark IDs.

The WorldEngine assigns stable IDs when it spawns landmarks at startup, for example `casualty-0`, `casualty-1`, and `corridor-0`. When a robot senses a landmark, it reads `ObjectData.id` from the sensor response and uses that value as the `LandmarkID` key in its local store.

## Consequences

Benefits:

1. Landmark state converges gradually across the swarm without requiring a central coordinator.
2. Casualty verification becomes a local quorum problem, while commit remains a separate Raft responsibility.
3. Delta-only sends reduce repeated payload volume compared with full-snapshot gossip.
4. Per-link bandwidth caps and casualty-first ordering prioritise high-value reports under constrained links.
5. The behaviour remains easy to reason about: periodic send, local merge, ID-based deduplication.

Trade-offs:

1. Because the store keys only by `LandmarkID`, conflicting sensor reports for the same ID are not automatically resolved.
2. If the WorldEngine ever emitted duplicate IDs for distinct objects, the store would merge them incorrectly.
3. Since receive-time forwarding is absent, convergence depends on periodic gossip rounds rather than immediate propagation.
4. Random peer fanout can delay delivery to a specific peer compared with deterministic schedules, although repeated rounds still converge.
5. Watermark-based deltas assume monotonic Lamport progress and can resend older semantic information if timestamp metadata changes.

## Notes

This design fits the current codebase and uses a practical middle ground between full-state anti-entropy and pure event-forwarding: periodic delta anti-entropy with local merge ownership. If stronger consistency or conflict resolution is needed later, the store will need explicit merge rules beyond simple ID-based lookup.