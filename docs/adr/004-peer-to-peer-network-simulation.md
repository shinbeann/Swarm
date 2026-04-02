# Robot Peer-to-Peer Communication via Simulated Network Constraints

## Context

The project goal is to simulate distributed computing concepts within a robot swarm. A core aspect is that robots need to communicate directly with one another, subject to realistic wireless network limitations (range, signal degradation). Rather than routing every inter-robot message through the World Engine, robots need a direct, peer-managed communication mechanism. However, for the simulation to be meaningful, these communications must still be constrained by the physics-based proximity data only the World Engine can authoritatively provide.

## Decision

We implemented a two-layer approach to robot-to-peer communication:

1. **`PeerService` in the Communications layer**: A new gRPC service (`PeerService` with a `SyncData` RPC) is defined in `robot.proto`. Each robot hosts this service on a secondary listener (`:50052`). This allows robots to communicate directly with each other without touching the World Engine.

2. **World Engine as the Network Oracle**: The World Engine's `GetNetworkData` endpoint calculates simulated network conditions based on Euclidean distance between all active tracked robots, returning per-peer metrics with the following behavior over a max communication range of `250.0` units:
   - **Bandwidth**: `50.0 Mbps` → `1.0 Mbps`
   - **Latency**: `5.0 ms` → `250.0 ms`
   - **Reliability**: currently fixed at `1.0` (distance-based reliability decay is disabled in code)

3. **Robot-side enforcement**: Network conditions are refreshed during the heartbeat loop, while gossip send attempts occur every 1 second. For each selected in-range peer send, the robot:
   - Applies a **reliability check** (`rand.Float64() > reliability`): if the packet fails, it is dropped entirely and logged.
   - Calculates **total delay** as `latency + (payloadSizeMB * 8 / bandwidth * 1000ms)` using the actual serialized payload size.
   - `time.Sleep`s for this duration before making the actual `SyncData` gRPC call to the peer.

This approach was chosen over routing through the World Engine to preserve a realistic distributed topology, while delegating constraint authority correctly to the simulation's central oracle.

## Consequences

**Easier:**
- The effect of network constraints is directly observable in Docker logs, with clear `[P2P Send]` and `[P2P Recv]` events showing delays, drops, and bandwidth.
- Robots expose a clear extension point: `SyncData`'s payload field can carry real application data (maps, survivor sightings) in the future.

**More Difficult:**
- Each robot container gets a new port dependency (`:50052`) which must be reachable by peers over the Docker network. This is handled implicitly because Docker Compose services can resolve each other by service name.
- The approach uses `time.Sleep` to simulate delay, which blocks the goroutine for realism but is computationally trivial at this scale.

**Maintenance Overhead:**
- If the network simulation model changes (e.g., non-linear decay, obstacle-based attenuation), the degradation formulas in `WorldEngine/main.go` and the documented constants in `PROJECT_SPECIFICATION.md` must both be updated.
