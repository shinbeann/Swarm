# World Engine Is the Authoritative Source for Obstacles and Sensor Data

## Context
Robots need to perceive their environment (walls, obstacles) to make navigation decisions. Two approaches were considered:
1. Encode obstacle positions statically in the Robot container at startup.
2. Have the World Engine serve obstacle data dynamically as part of sensor simulation.

The project's core principle is that the World Engine is the **single source of truth** for all physical simulation data.

## Decision
The World Engine spawns a fixed set of **boundary wall obstacles** at startup (four rectangular walls at the edges of the 1000x1000 world). These are stored in the server's `walls` field.

- The `GetEnvironmentData` RPC returns the **complete list of all obstacles** to the Visualiser so it can render the static environment.
- The `GetSensorData` RPC performs a **proximity check** per robot (using bounding-box/circle distance) and returns only the obstacles within a defined sensor range. This prevents robots from having global knowledge of all obstacles.

Robots then use the sensor data response locally: if any `obstacle` type object is within 5 units in the response, the robot reverses its heading by ~π radians on the next control loop tick.

## Consequences
- **Easier**: The Robot logic is kept simple — it just reacts to what the World Engine tells it. Adding new obstacle types or changing detection radius only requires WorldEngine changes.
- **Easier**: Centralizing obstacle truth makes the Visualiser trivial to implement — it just renders whatever the World Engine says exists.
- **Harder**: Every robot polls `GetSensorData` every 200ms, creating O(robots × obstacles) proximity checks per cycle on the World Engine. For a large swarm or map, a spatial index (e.g., quadtree) would be needed.
- **Overhead**: The 5-unit avoidance trigger combined with a simple heading reversal can cause oscillation if a robot bounces between two close obstacles. More sophisticated steering (e.g., potential fields) should be explored later.
