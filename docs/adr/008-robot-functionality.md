# Robot Functionality: Movement Decisions From Knowledge State and Raft-Backed Quadrant Rotation

## Context

ADR-007 already documents how gossip is received, merged, and stored. This ADR records the separate movement behavior that uses that local state, plus the Raft-backed swarm relocation policy that coordinates quadrant movement across the cluster.

## Decision

Robots use the local knowledge store to drive casualty-seeking movement decisions, but the Raft leader also periodically publishes a swarm relocation decision that keeps the formation rotating through the world quadrants.

- `requestMovement` executes every 2 seconds.
- It first calls `store.UnverifiedCasualties(selfID)` and, if non-empty, selects the closest casualty via `closestCasualty`.
- It then issues a `MoveToPosition` request toward that casualty position (`target.Location`), with heading aimed at the casualty.
- If movement succeeds, it logs the diversion and does not execute random movement in that tick.
- If a committed swarm relocation directive is active, it supersedes random wandering until the robot reaches the committed target within the configured tolerance envelope.
- Once the robot is inside the tolerance envelope, the directive is cleared and the robot immediately resumes normal exploration on later movement ticks.
- If there are no unverified casualties and no active committed swarm relocation directive, it executes obstacle-aware random targeting:
  - Picks a random distance (100–300 units) and angle.
  - If near an obstacle, it chooses an angle away from obstacle plus small jitter.
  - Clamps target coordinates to map bounds (50–950).
  - Sends `MoveToPosition` to this random target.

- The 1000×1000 world is divided into four quadrants. Robots start in the top-left quadrant.
- Every 30 seconds, the Raft leader appends a `swarm_quadrant_move` log entry that rotates the swarm through the fixed cycle top-left -> top-right -> bottom-right -> bottom-left -> repeat.
- The new relocation decision is commit-gated: followers do not react until the leader’s log entry is replicated and committed.
- The leader also buffers the next relocation decision window so movement time does not consume the 30-second decision interval.

- A casualty becomes “verified” when it has ≥`VerificationQuorum` distinct reporters in `KnowledgeStore`.
- Once verified, it no longer appears in `UnverifiedCasualties`, and robots stop diverting specifically for it.
- **Raft Log Integration**: During `syncRaftWithPeers`, the Raft leader scans for verified-but-uncommitted casualties and appends a `casualty_verified` entry for each one.
  - During the same Raft tick, the leader also appends a `swarm_quadrant_move` entry when the relocation cadence allows it.
  - Once that Raft entry is replicated and committed, `ApplyCasualtyVerified` marks the casualty as `Committed`.
  - Once a `swarm_quadrant_move` entry is committed, all robots adopt that target and move toward it until they are within the configured tolerance.
  - Non-leaders do not append entries, but they still apply committed entries when they arrive through Raft.

## Consequences

- Movement is goal-prioritized: casualty verification overrides both quadrant rotation and random exploration.
- The swarm still focuses effort on recently discovered, not-yet-verified casualties without a separate controller, but it now also performs periodic Raft-coordinated relocation across the world.
- **Consensus Durability**: Committed casualties are recorded in the Raft log by the leader, providing a shared, consistent history of verified events across the cluster.
- **Subsystem Separation**: The Gossip engine and Raft consensus layer remain decoupled because the leader periodically scans verified-but-uncommitted casualties and relocation cadence state during `syncRaftWithPeers`, avoiding shared mutable handoff state and keeping Raft work off the gossip path.
- **Coordinated Exploration**: The swarm can be moved as a group into different quadrants without changing the WorldEngine movement API because the leader commits an absolute target and followers derive their movement from the applied log entry.
- Gossip handling, landmark matching, and route merging remain documented in ADR-007.
