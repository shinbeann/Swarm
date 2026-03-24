# Dedicated Raft Service with Runtime Status Endpoint

## Context

Robot-to-robot communication already existed through PeerService SyncData, but consensus concerns (leader election and log replication) were mixed with application-level gossip payloads. This coupling made it harder to reason about correctness and evolve the Raft implementation.

At the same time, debugging distributed behavior inside scaled Docker robot containers required direct runtime visibility into role, term, and log state.

## Decision

Raft coordination is separated into a dedicated RaftService hosted by each robot:

- RequestVote handles candidate vote solicitation.
- AppendEntries handles both heartbeat and replicated log entry flow.
- PeerService remains dedicated to gossip payload sync and Lamport clock updates.

Robot runtime now includes in-memory Raft state and role transitions:

- Follower to candidate transition on randomized election timeout.
- Candidate vote requests over RaftService and leader election on majority.
- Leader AppendEntries heartbeats plus a periodic append-entry command every 10 seconds.
- First replicated command type is leader_ping, including Unix-millisecond timestamp metadata.

A local HTTP observability endpoint is added to each Robot instance:

- GET /status (default :8081, configurable via -status-addr).
- Returns runtime snapshot with role, term, known leader, log length, commit index, and key timing values.

## Consequences

Easier:

- Consensus and gossip concerns are cleanly separated at the RPC contract and handler level.
- Runtime diagnosis of split-brain/election/replication behavior is much simpler through the status endpoint.
- Existing constrained network simulation still governs both gossip and Raft traffic paths.

More difficult:

- Running an additional gRPC service per robot (`:50053`) increases operational surface area.
- Raft and gossip traffic now have separate cadences and retry behavior that must be tuned independently.

Maintenance overhead:

- Any change to Raft message fields requires protobuf regeneration and synchronized updates in robot handling logic.
- Documentation and tests must be kept aligned as consensus semantics evolve beyond the initial leader_ping command.
