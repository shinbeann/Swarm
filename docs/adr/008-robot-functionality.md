# Robot Functionality: Movement Decisions From Knowledge State

## Context

ADR-007 already documents how gossip is received, merged, and stored. This ADR records the separate movement behavior that uses that local state.

## Decision

Robots use the local knowledge store to drive movement decisions.

- `requestMovement` executes every 2 seconds.
- It first calls `store.UnverifiedCasualties(selfID)` and, if non-empty, selects the closest casualty via `closestCasualty`.
- It then issues a `MoveToPosition` request toward that casualty position (`target.Location`), with heading aimed at the casualty.
- If movement succeeds, it logs the diversion and does not execute random movement in that tick.
- If there are no casualties below verification quorum, it executes obstacle-aware random targeting:
  - Picks a random distance (100–300 units) and angle.
  - If near an obstacle, it chooses an angle away from obstacle plus small jitter.
  - Clamps target coordinates to map bounds (50–950).
  - Sends `MoveToPosition` to this random target.

- A casualty becomes “verified” when it has ≥`VerificationQuorum` distinct reporters in `KnowledgeStore`.
- Once verified, it no longer appears in `UnverifiedCasualties`, and robots stop diverting specifically for it.
- **Raft Log Integration**: During `syncRaftWithPeers`, the Raft leader scans for verified-but-uncommitted casualties and appends a `casualty_verified` entry for each one.
  - Once that Raft entry is replicated and committed, `ApplyCasualtyVerified` marks the casualty as `Committed`.
  - Non-leaders do not append entries, but they still apply committed entries when they arrive through Raft.

## Consequences

- Movement is goal-prioritized: casualty verification overrides random exploration.
- The swarm naturally focuses effort on recently discovered, not-yet-verified casualties without a separate controller.
- **Consensus Durability**: Committed casualties are recorded in the Raft log by the leader, providing a shared, consistent history of verified events across the cluster.
- **Subsystem Separation**: The Gossip engine and Raft consensus layer remain decoupled because the leader periodically scans verified-but-uncommitted casualties during `syncRaftWithPeers`, avoiding shared mutable handoff state and keeping Raft work off the gossip path.
- Gossip handling, landmark matching, and route merging remain documented in ADR-007.
