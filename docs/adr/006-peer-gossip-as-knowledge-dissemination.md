# PeerService as Gossip Knowledge Dissemination Channel

## Context

`PeerService.SyncData` was originally a generic transport for Lamport clock synchronisation and simulated large-payload transfers between robots. It carried a dummy `bytes payload` field with no structured semantics.

Separately, the `leader-feat` branch introduced `RaftService` as a dedicated consensus channel, deliberately keeping it separate from `PeerService` so that gossip and consensus traffic would not intermesh at the RPC contract level.

With landmark discovery added to the World Engine and robot knowledge stores, there was now a need for robots to actually share discovered knowledge with peers. The question was: should `PeerService` evolve to carry structured knowledge, or should a new service be introduced?

## Decision

`PeerService` is evolved into the general **gossip knowledge dissemination channel**. It carries:

- Landmark discovery entries serialised as a `GossipMessage` (JSON-encoded) in `PeerSyncRequest.payload`.
- Lamport clock values as **metadata only** — used for ordering hints, not correctness-critical ordering.
- Future shared robot knowledge in the same extensible payload format.

The service name `PeerService` is kept for compatibility and because it accurately describes the channel: direct robot-to-robot peer communication.

### Payload model
- `GossipMessage` contains a sender ID, a Lamport timestamp, and a list of `LandmarkEntry` records.
- Each `LandmarkEntry` carries: `LandmarkID`, `LandmarkType`, `Location`, and a `Reporters` map of `RobotID → Lamport timestamp`.
- Deduplication in `KnowledgeStore` is by **ID + timestamp**: an entry is updated only if the incoming timestamp is newer. Reporter provenance is preserved.

### Gossip architecture inside the Robot
- `KnowledgeStore`: concurrent map of discovered landmark entries, protected by `sync.RWMutex`.
- `NeighbourRegistry`: tracks active peers based on incoming `HandlePeerSync` calls and outgoing `GetNetworkData` results. Entries expire after 3 seconds of inactivity.
- `GossipEngine`: background 1-second ticker that selects a random active neighbour and pushes all known entries via `sendGossipMessage`. Runs as an independent goroutine and is stopped gracefully on shutdown.

### Boundary with RaftService
`PeerService.SyncData` must **never** carry Raft vote or log replication payloads. `RaftService` (port `:50053`) remains the exclusive channel for consensus traffic. Both services share the WorldEngine-derived network topology for bandwidth, latency, and reliability simulation, but have independent cadence and handlers.

## Consequences

Easier:
- Landmark knowledge dissemination is a natural extension of an existing service; no new proto service registration required.
- The gossip payload is extensible: future knowledge types (e.g., robot positions, hazard zones) can be added to `GossipMessage` without changing the service contract.
- `PeerService` and `RaftService` remain cleanly separated at the RPC layer, preventing coupling between gossip correctness and consensus correctness.

More difficult:
- The payload field in `PeerSyncRequest` is now semantically structured (JSON `GossipMessage`) rather than opaque bytes. Code that sends non-gossip payloads must handle the case where `HandlePeerSync` cannot unmarshal the payload.
- The `GossipEngine` goroutine adds a new concurrency boundary that must be shut down correctly on robot stop.

Maintenance overhead:
- Changes to `LandmarkEntry` or `GossipMessage` fields require coordinated updates to `types.go`, `gossip_engine.go`, `knowledge_store.go`, and any consumers.
- Documentation and tests should be kept aligned as new knowledge types are added to the gossip payload.
