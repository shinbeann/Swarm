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

The suite is broken down into Raft-focused tests and supporting Robot/system tests.

### Raft-focused test files

| File | Focus Area | Covered Scenarios |
|---|---|---|
| `raft_candidate_timing_test.go` | Election pacing and state transitions | Candidate vote retry backoff, and retry timer reset when a candidate steps down to follower. |
| `raft_casualty_test.go` | Raft-driven casualty verification | Converting distinct casualty reports into a replicated verification entry once the quorum is reached. |
| `raft_election_test.go` | Elections and Candidate Mechanics | Static stability, stale leader demotion, stale log candidate rejection, split vote resolution, term safety demotions, double-vote prevention. |
| `raft_leader_failure_test.go` | Network Partitions and Leader Death | Resolving majority-replicated but uncommitted logs upon leader death, overwriting minority-replicated logs when partitioned, and a minority follower with uncommitted logs successfully getting elected and replicating those uncommitted logs to the rest of the cluster. |
| `raft_leader_init_test.go` | Term Beginnings | Leader state initialization (`nextIndex`/`matchIndex`), successful heartbeats, failure to out-of-sync followers, and multi-step nextIndex backtracking. |
| `raft_follower_test.go` | Follower Reactivity | Rejection of laggy prevLogIndex, catching up on missing logs, correct math handling for updating `commitIndex`, and idempotency (duplicate drops). |
| `raft_term_safety_test.go` | Universal Protection | Automatic follower transition when observing higher terms in responses or requests, rejecting requests from lower terms. |
| `raft_log_consistency_test.go` | Append Math | Exact log appending, excess entry truncation, conflict resolutions, and Raft's edge-case rule forbidding direct commits of previous-term entries. |

### Other Robot/system test files

| File | Focus Area | Covered Scenarios |
|---|---|---|
| `robot_behavior_test.go` | Landmark Verification and Movement | First landmark sighting records a self-report, a third distinct reporter verifies a casualty, gossip reception merges peer reports and preserves the sender's embedded reporter provenance, and movement prioritizes unverified casualties before falling back to normal exploration. |
| `routing_table_test.go` | Mesh routing and path discovery | Direct neighbour routes, advertisement merging, shortest-path replacement, route expiry, and reachable-node enumeration. |
| `network_test.go` | Network simulation and partition behavior | Packet latency/bandwidth constraints, topology convergence, partition safety, and TTL-based mesh drops. |


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
   - gossip payloads preserve embedded eyewitness reporter provenance rather than crediting the relay sender automatically

# Details

## raft_candidate_timing_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestCandidateVoteRetryPacing | Verifies that a candidate does not spam vote requests and respects the retry interval between rounds. | A robot is placed in candidate state and `shouldSendCandidateVotesLocked` is checked at several simulated time offsets. | The first vote round is allowed immediately, the next call is throttled before the retry window elapses, and a later call is allowed again after the interval passes. |
| TestCandidateVoteRetryResetOnFollowerTransition | Confirms that the vote retry timer is cleared when a candidate steps down to follower. | A robot starts as a candidate, sends a vote round, then transitions to follower after observing a higher term. | `lastVoteRequestAt` is reset to the zero time value after the follower transition. |

## raft_casualty_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftCasualtyVerification | Confirms that casualty reports become verified only after the required quorum of distinct reporters is reached. | The robot is forced into leader state, then receives three casualty reports for the same ID from three different peers before `syncRaftWithPeers` is invoked. | The Raft log gains a `casualty_verified` entry containing the casualty ID and type once the third distinct report is processed. |

## raft_election_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftElectionBasic | Verifies the normal election path where a candidate wins a majority and becomes leader. | A five-node cluster is prepared, `r1` is forced into candidate state, votes are delivered from `r2` and `r3`, and `r4` receives a heartbeat from the winning leader. | `r1` becomes leader, the other nodes remain followers, and heartbeat processing refreshes the follower’s last-leader timestamp. |
| TestRaftStaleLeaderDemotion | Ensures a leader with an older term steps down when it encounters a newer term. | `r1` starts as leader in term 1, `r2` is leader in term 2, and `r1` sends a heartbeat-style AppendEntries to `r2`. | `r2` rejects the stale leader, while `r1` demotes to follower and updates its term to 2. |
| TestRaftElectionStaleLogRejection | Prevents a candidate with a stale or shorter log from winning an election. | `r2` and `r3` each hold a one-entry log in term 1, while `r1` has an empty log and starts an election in term 2. | `r2` rejects `r1`’s vote request and `r1` fails to become leader. |
| TestRaftSplitVote | Exercises a split-vote scenario and verifies that a later term can resolve it. | A four-node cluster is created, `r1` and `r2` both start elections in term 2, the remaining votes split evenly, and `r1` retries in term 3. | Neither candidate wins in term 2, and `r1` wins the election in term 3. |
| TestRaftCandidateStepDownOnAppendEntries | Ensures a candidate steps down when it receives a valid AppendEntries request from a leader in the same term. | `r1` is placed in candidate state for term 2 and `r2` acts as leader in term 2, then sends a heartbeat to `r1`. | `r1` accepts the heartbeat and becomes a follower. |
| TestRaftCandidateStepDownOnHigherTermVoteRequest | Verifies that a candidate yields when a vote request arrives from a higher term. | `r1` is a candidate in term 2 and `r2` sends a vote request for term 3. | `r1` grants the vote, steps down to follower, and advances its term to 3. |
| TestRaftDoubleVotePrevention | Confirms that a follower will not vote for two different candidates in the same term. | `r3` receives vote requests from `r1` and `r2` for the same term in sequence. | `r3` grants the first request and rejects the second request for that term. |

## raft_follower_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftFollowerLagRejection | Verifies that a follower rejects AppendEntries when the leader’s previous log index is beyond the follower’s local history. | `r1` is a leader with three log entries and `r2` is empty; `r1` sends an append request with `prevLogIndex=2`. | `r2` rejects the request because it cannot satisfy the previous-entry check. |
| TestRaftFollowerCatchupAndCommitUpdate | Validates that a lagging follower can receive missing entries and then advance its commit index. | `r1` is leader with two committed entries and `r2` only has the first entry; replication is attempted from `r1` to `r2`. | `r2` catches up to both entries and advances `commitIndex` to 1. |
| TestRaftFollowerHeartbeatIdempotency | Ensures repeated empty heartbeats do not disturb follower log state. | `r1` sends five consecutive empty heartbeats to `r2`. | Every heartbeat succeeds and `r2`’s log length does not change. |
| TestRaftFollowerDuplicateAppendIdempotency | Verifies that retrying the same AppendEntries request does not duplicate entries on the follower. | `r1` sends the same AppendEntries request to `r2` twice. | Both requests succeed and `r2` still contains exactly one copy of the appended entry. |

## raft_leader_failure_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftLeaderFailureMajorityReplicatedUncommitted | Confirms that an uncommitted entry replicated to a majority can be committed by the next leader through a current-term entry. | `r1` appends an entry to itself and two followers, then fails; `r2` becomes leader in term 2 and appends a new current-term entry. | `r2` commits both the inherited entry and its own new entry. |
| TestRaftLeaderFailureMinorityOverwritten | Ensures entries that only reached a minority can be overwritten by a new leader. | `r1` replicates an entry only to `r2`, then fails; `r3` becomes the new leader with an empty log. | `r2`’s minority-only entry is replaced by `r3`’s new replicated entry. |
| TestRaftLeaderFailureMinorityElectedReplicates | Verifies that a node holding minority-only uncommitted logs can still win later and spread those logs cluster-wide. | `r1` writes to `r2` only, fails, and `r2` later wins an election in term 3 while still carrying the uncommitted entry. | `r2` becomes leader and backtracks far enough to replicate the old entry to `r3` and `r4`, then commits it with a current-term entry. |

## raft_leader_init_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftLeaderInitIndices | Verifies that leader transition initializes `nextIndex` and `matchIndex` correctly. | `r1` has two log entries and is forced through an election to become leader. | Followers get `nextIndex=2`, followers start with `matchIndex=-1`, and the leader’s own `matchIndex` points at its last log entry. |
| TestRaftLeaderHeartbeatInSync | Confirms that a leader can send an empty heartbeat to a follower that is already in sync. | `r1` is leader and `r2` already matches its log with one entry. | The heartbeat to `r2` carries no entries and succeeds. |
| TestRaftLeaderHeartbeatOutOfSync | Validates that the leader detects a follower whose log is not actually in sync. | `r1` assumes `r2` is up to date, but `r2` is really empty when the heartbeat is sent. | `r2` rejects the append because of the log mismatch. |
| TestRaftLeaderBacktracking | Tests the leader’s ability to backtrack `nextIndex` repeatedly until a very lagged follower accepts replication. | `r1` is leader with three entries, `r2` starts empty, and the leader initially guesses an overly large `nextIndex`. | After multiple retries, the leader backtracks to `prevLogIndex=-1`, `r2` accepts the append, and all three entries are replicated. |

## raft_log_consistency_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftLogConsistencyTruncation | Verifies that a follower truncates divergent entries and adopts the leader’s branch when a conflict is detected. | `r2` starts with three conflicting term-1 entries, `r1` is leader with a different term-2 entry at the conflicting index, and the leader retries after rejection. | `r2` first rejects the conflicting append, then accepts the retried append, truncates the old suffix, and replaces it with the leader’s entry. |
| TestRaftLogConsistencyIndirectCommit | Ensures that a leader cannot directly commit an older-term entry until a current-term entry is committed first. | `r2` becomes leader with a carried-over older-term entry and replicates it to `r3` before adding any new current-term entry. | `commitIndex` stays at `-1` until a current-term entry is appended and replicated, after which both entries become committed. |
| TestRaftLogConsistencyFollowerCommit | Confirms that a follower advances its commit index from the leader’s `leaderCommit` value. | `r1` appends two entries, replicates them to `r2`, commits index 1, and then sends a heartbeat containing the commit index. | `r2` advances its `commitIndex` to 1 to match the leader. |

## raft_term_safety_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRaftTermSafetyLeaderDemotionOnResponse | Ensures a leader steps down when a peer response reveals a higher term. | A leader receives a response whose term is greater than its own. | The node demotes to follower and updates its term to the higher value. |
| TestRaftTermSafetyLeaderDemotionOnVoteRequest | Ensures a leader steps down when it receives a vote request from a higher term. | A leader is given a vote request that carries a newer term. | The node steps down to follower and adopts the higher term before responding. |
| TestRaftTermSafetyRejectLowerTermAppend | Confirms that a node rejects AppendEntries requests from a stale term. | A node receives an AppendEntries request with a term lower than its own. | The request is rejected and the receiver keeps its current term and role. |
| TestRaftTermSafetyRejectLowerTermVote | Confirms that a node rejects vote requests from a stale term. | A node receives a RequestVote message with a term lower than its own. | The request is rejected and the node retains its current term and state. |

## robot_behavior_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestKnowledgeStoreVerificationLifecycle | Verifies the casualty verification lifecycle in the knowledge store. | The same casualty is reported by three distinct robots and then reported again by one of the existing reporters. | The casualty remains unverified after the first two reports, becomes verified on the third distinct report, and the duplicate report does not add a new reporter or change the verified state. |
| TestRobotOnPeerSyncVerifiesCasualtyOnThirdDistinctReporter | Confirms that gossip reception marks a casualty as verified once the third distinct reporter is observed. | The robot receives peer-sync payloads carrying the same casualty from three different peers. | After the third distinct reporter arrives, the casualty is marked verified and the stored reporter set has size 3. |
| TestRobotOnPeerSyncPreservesEyewitnessProvenanceAcrossRelay | Ensures relayed gossip preserves original eyewitness provenance instead of crediting the relay as a new witness. | The robot receives gossip entries where the embedded reporter set already identifies the original eyewitness, but the payload is relayed through a different intermediary. | The relay sender is not added as a reporter; only the original eyewitness provenance is retained. |
| TestRobotTickRecordsFirstLandmarkSighting | Verifies that a first-time landmark sighting is stored as an unverified self-report. | The robot receives sensor data for a landmark and then runs its tick/update cycle. | The landmark is stored with matching location data, marked unverified, and attributed to the robot itself as reporter. |
| TestRequestMovementPrefersUnverifiedCasualtyAndStopsAfterVerification | Confirms that movement targeting prioritizes unverified casualties and changes once the casualty becomes verified. | The robot starts with a casualty reported by two peers, `requestMovement()` is called, then a third report is added and `requestMovement()` is called again. | The first movement targets the unverified casualty, and after verification the casualty is removed from the unverified priority set so a different target is chosen. |
| TestGossipEngineDeltaSkipsPeersWithoutNewEntries | Validates that the gossip engine avoids resending data to peers when there are no new delta entries. | The engine records one discovery, gossips to peers, gossips again without new discoveries, and then records a second discovery. | The first and third gossip rounds send data, while the middle round sends nothing because the watermark has already advanced. |
| TestGossipEngineDeltaPrioritizesCasualtiesWithinBandwidthCap | Ensures casualty entries are prioritized when the gossip budget allows only a single entry. | The engine holds multiple discovery types and the peer’s bandwidth cap allows only one entry per round. | The casualty entry is sent first and lower-priority entries are deferred. |
| TestGossipEngineOnReceiveMakesRobotIndependentDeltaSource | Verifies that received gossip becomes part of the robot’s next outbound delta stream. | The robot receives a gossip payload with one entry and then runs its next gossip round. | The received entry is forwarded to other peers on the next round, and the following round is skipped because the watermark prevents resending the same delta. |

## routing_table_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestRoutingTableDirectNeighbour | Verifies that a direct neighbour route is recorded and retrievable. | Node A records node B as a direct neighbour. | `GetRoute("B")` returns a one-hop route whose next hop is B. |
| TestRoutingTableMergeAdvertisement | Validates that an advertisement from a neighbour is merged into multi-hop paths. | A knows B directly, then B advertises C and D and A merges that advertisement. | A keeps B at one hop, learns C at two hops via B, and learns D at three hops via B. |
| TestRoutingTableShorterPathWins | Ensures that a shorter route replaces a longer existing route. | B first advertises D via a longer path, then E advertises D via a shorter path. | The route through E replaces the longer route and becomes the preferred path. |
| TestRoutingTablePruneExpired | Confirms that stale routes are removed after their expiry window. | Routes are created and then artificially aged beyond the prune threshold before pruning is run. | All expired routes are removed and the table size becomes zero. |
| TestRoutingTableBuildAdvertisement | Verifies that outgoing advertisements include self, direct neighbours, and indirect routes with the correct hop counts. | A knows B directly and learns C indirectly through B. | The generated advertisement includes A at 0 hops, B at 1 hop, and C at 2 hops. |
| TestRoutingTableSelfIDIgnored | Ensures that the routing table never creates a route to the local node itself. | The node is recorded as its own neighbour and also appears in an advertisement. | No self-route is stored and the table remains empty. |
| TestRoutingTableGetAllReachable | Confirms that all direct and indirect reachable nodes are returned. | The table is populated with direct neighbours and indirect neighbours learned through advertisements. | `GetAllReachable()` returns every reachable node that was added. |

## network_test.go

| Test case | Objective | Key setup details | Expected result |
|---|---|---|---|
| TestMultiHopPacketLatency | Verifies that packet delivery time reflects configured latency and transfer cost. | A network constraint is applied with latency, bandwidth, payload size, reliability, and mesh-type routing parameters. | The measured delay matches the expected latency plus transfer time and the delivery is successful. |
| TestRoutingConvergenceAndTopologyChanges | Validates that routing updates track topology changes over time. | A linear r1-r2-r3-r4 topology is created, a direct r1-r4 link is added, and then r4 is aged out via pruning. | The route to r4 improves from three hops to one hop after the direct link is added, then disappears when the link expires. |
| TestNetworkPartitionsAndRaftSplitBrain | Confirms that a majority partition can continue operating while a minority partition cannot, and that healing the partition restores term safety. | A five-node cluster is split into a three-node majority and a two-node minority while `r1` begins as leader. | The majority continues to replicate, the minority cannot elect a competing leader, and when the partition heals the stale leader steps down after observing the higher term. |
| TestMeshTTLDrops | Verifies that the mesh router drops packets whose TTL expires before reaching the destination. | A routed message with TTL 1 is sent to a non-self destination through the mesh router. | The message is not delivered and is reported as dropped because the TTL is exhausted. |

## Scope Notes

The Raft tests are intentionally deterministic: they drive the state machine directly instead of relying on live event loops or real network servers. The robot behavior tests similarly stay local by using fake clients and direct gossip injection rather than a full WorldEngine simulation.

The current suite covers static consensus behavior and localized robot behavior transitions. Dynamic long-running swarm movement, recovery under changing topology, and broader end-to-end convergence remain good candidates for future expansion.

