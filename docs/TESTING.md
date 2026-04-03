# Raft Consensus Algorithm Test Suite

## Overview

Documenting the test suite and documentation for the Raft consensus implementation in `Robot/robot.go`.  

Therefore, the actual Go test files are located alongside `robot.go` in the parent `Robot` directory, prefixed with `raft_` to clearly distinguish them.

## Test Files Location

- `Robot/raft_test_helpers_test.go`: Shared initialization and validation helpers.
- `Robot/raft_election_test.go`: Node voting, term progression, split votes.
- `Robot/raft_leader_failure_test.go`: Majority/minority log replication and leader death.
- `Robot/raft_leader_init_test.go`: Leader state transitions, `nextIndex`/`matchIndex` setup.
- `Robot/raft_follower_test.go`: Follower lag, catch-up, and heartbeat idempotency.
- `Robot/raft_term_safety_test.go`: Step-down on higher terms, rejection of lower terms.
- `Robot/raft_log_consistency_test.go`: Log truncation, conflicts, missing entries, commit math.
- `Robot/robot_behavior_test.go`: Landmark sighting, gossip-driven verification, and movement prioritization.

## Implemented Scenarios

The suite is broken down strictly by behavior domains:

| File | Focus Area | Covered Scenarios |
|---|---|---|
| `raft_election_test.go` | Elections and Candidate Mechanics | Static stability, stale leader demotion, stale log candidate rejection, split vote resolution, term safety demotions, double-vote prevention. |
| `raft_leader_failure_test.go` | Network Partitions and Leader Death | Resolving majority-replicated but uncommitted logs upon leader death, overwriting minority-replicated logs when partitioned, and a minority follower with uncommitted logs successfully getting elected and replicating those uncommitted logs to the rest of the cluster. |
| `raft_leader_init_test.go` | Term Beginnings | Leader state initialization (`nextIndex`/`matchIndex`), successful heartbeats, failure to out-of-sync followers, and multi-step nextIndex backtracking. |
| `raft_follower_test.go` | Follower Reactivity | Rejection of laggy prevLogIndex, catching up on missing logs, correct math handling for updating `commitIndex`, and idempotency (duplicate drops). |
| `raft_term_safety_test.go` | Universal Protection | Automatic Follower transition when observing higher terms in responses or requests, rejecting requests from lower terms. |
| `raft_log_consistency_test.go` | Append Math | Exact log appending, excess entry truncation, conflict resolutions, and Raft's edge-case rule forbidding direct commits of previous-term entries. |

| `robot_behavior_test.go` | Landmark Verification and Movement | First landmark sighting records a self-report, a third distinct reporter verifies a casualty, gossip reception merges peer reports into local state, and movement prioritizes unverified casualties before falling back to normal exploration. |


NOTE: The test suite proves what happens with a static raft system. But with dynamically moving robots, topologies and network conditions change. The behaviour under such dynamism is not yet tested. Furthermore, behaviour might be correct, but the performance, disruption and recovery time under such conditions is also not yet measured. These are important areas for future test expansion.  

NOTE: The Robot behaviour suite now covers the local consequences of landmark discovery and gossip-fed verification. It still uses focused unit-style checks rather than full simulation runs, so broader swarm-level convergence and long-running movement dynamics remain future test candidates.

## Test Design Principles

The tests in this suite follow a set of core principles to ensure they are deterministic, reliable, and easy to reason about:

1. **State Machine Driving**:
   We do not rely on the actual running event loops (`Run`, `tickRaft`) or the networking stack (`gossipEngine`, `MeshRouter`) during tests. Instead, we manually drive the Raft state machine by calling its core public/internal APIs directly:
   - `HandleRequestVote` / `handleVoteResponse`
   - `HandleAppendEntries` / `handleAppendResponse`
   - `buildVoteRequest` / `buildAppendEntriesRequestForPeer`
   - `maybeStartElection`

2. **Timer Abstraction**:
   Relying on real-time timers (`time.Sleep`, tickers) introduces flakiness and slows down the suite. All tests bypass natural time progressions. Instead, when we need a node to start an election, we manipulate its internal `lastLeaderSeenAt` timestamp via a test helper (`forceElectionTimeout`) to make it immediately eligible for an election upon calling `maybeStartElection()`.

3. **In-Memory Networking Proxy**:
   Since we don't spin up real gRPC servers, all simulated "network messages" between nodes are handled by directly passing the protobuf request objects returned by the sender's `build*` functions into the receiver's `Handle*` functions, and then passing the response back.

4. **Topology Seeding**:
   The Raft implementation relies on the localized mesh routing table (`RoutingTable`) to compute the cluster topology dynamically. To simulate a fully connected or partially connected network, test helpers (`setupCluster`) explicitly seed these routing tables so that `updateRaftPeerTopology` returns the correct, expected set of peers.

5. **Isolable Validation & Edge Cases**:
   Each test specifically targets an edge condition or phase of the Raft algorithm (e.g. term safety, logs backtracking) making failures easy to diagnose. Tests never depend on the execution order of other tests.

6. **Robot Behavior from Local State**:
   The new Robot behavior tests avoid the live WorldEngine and instead drive the robot through deterministic local state transitions:
   - sensor sightings are injected through a fake RobotService client
   - gossip verification is driven directly through `OnPeerSync`
   - movement is asserted by capturing `MoveToPosition` requests from a fake client
