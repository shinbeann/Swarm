# Robot Functionality: Movement Decisions From Knowledge State

## Context

ADR-007 already documents how gossip is received, merged, and stored. This ADR records the separate movement behavior that uses that local state.

## Decision

Robots use the local knowledge store to drive movement decisions.

- `requestMovement` executes every 2 seconds.
- It first calls `store.UnverifiedCasualties(selfID)` and, if non-empty, selects the closest casualty via `closestCasualty`.
- It then issues a `MoveToPosition` request toward that casualty position (`target.Location`), with heading aimed at the casualty.
- If movement succeeds, it logs the diversion and does not execute random movement in that tick.
- If there are no unverified casualties, it executes obstacle-aware random targeting:
  - Picks a random distance (100–300 units) and angle.
  - If near an obstacle, it chooses an angle away from obstacle plus small jitter.
  - Clamps target coordinates to map bounds (50–950).
  - Sends `MoveToPosition` to this random target.

- A casualty becomes “verified” when it has ≥`VerificationQuorum` distinct reporters in `KnowledgeStore`.
- Once verified, it no longer appears in `UnverifiedCasualties`, and robots stop diverting specifically for it.

## Consequences

- Movement is goal-prioritized: casualty verification overrides random exploration.
- The swarm naturally focuses effort on recently discovered, not-yet-verified casualties without a separate controller.
- Gossip handling, landmark matching, and route merging remain documented in ADR-007.
