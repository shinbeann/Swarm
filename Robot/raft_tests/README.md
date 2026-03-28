# Raft Consensus Algorithm Test Suite

## Overview

This directory (`raft_tests`) contains the test suite and documentation for the Raft consensus implementation in `Robot/robot.go`. Because the Swarm Project's `Robot` is part of `package main` and relies on unexported methods (e.g., `maybeStartElection`, `handleVoteResponse`, `advanceCommitIndexLocked`), testing it from a separate package folder is anti-idiomatic in Go.

Therefore, the actual Go test files are located alongside `robot.go` in the parent `Robot` directory, prefixed with `raft_` to clearly distinguish them.

## Test Files Location

- `Robot/raft_test_helpers_test.go`: Shared initialization and validation helpers.
- `Robot/raft_election_test.go`: Node voting, term progression, split votes.
- `Robot/raft_leader_failure_test.go`: Majority/minority log replication and leader death.
- `Robot/raft_leader_init_test.go`: Leader state transitions, `nextIndex`/`matchIndex` setup.
- `Robot/raft_follower_test.go`: Follower lag, catch-up, and heartbeat idempotency.
- `Robot/raft_term_safety_test.go`: Step-down on higher terms, rejection of lower terms.
- `Robot/raft_log_consistency_test.go`: Log truncation, conflicts, missing entries, commit math.

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
