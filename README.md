# Swarm Project

A 2D simulation of a robot swarm performing distributed search and rescue missions in a disaster area. 

This project explores distributed computing concepts. The robots form a swarm to communicate information, map out a coordinate-based environment, and find survivors. The simulator visualizes the swarm's behavior resulting from these distributed algorithms.

## Project Architecture

The architecture relies on a **Hub and Spoke** pattern for simulation physics, while preserving distributed concepts for the robots' logic:
* **World Engine (Go/gRPC)**: The central authority. It simulates the 2D plane, generates boundary obstacles, processes robot intents (movement), and provides restricted, "faked" localized sensor data and simulated network constraint conditions to individual robots.
* **Robots (Go/gRPC)**: Independent agents running individual control loops. They query the World Engine for sensor data/obstacles and submit movement requests. They send periodic heartbeats to register their existence. 
* **Communications**: Protobuf definitions defining the strictly enforced gRPC contracts between the World, Robots, and Visualiser.
* **Visualiser (Go/React/PixiJS)**: A high-performance web dashboard. It utilizes a **Backend-for-Frontend (BFF)** architecture where a Go proxy subscribes to the World Engine via gRPC and streams environment limits, obstacles, and robot telemetry to a Vite+React web UI over WebSockets.

## Prerequisites

* Docker
* Docker Compose

## Getting Started

The entire swarm simulation is containerized and orchestrated via Docker Compose.

1. **Clone the repository.**
2. **Build and start the simulation:**
   ```bash
   docker compose up --build
   ```
   This will spin up:
   - 1 `world-engine` container on port `50051`.
   - 2 `robot` instances (extensible in the `docker-compose.yml`) navigating the simulation.
   - 1 `visualiser` proxy container on port `8080`, exposing the web UI on port `3000`.

3. **View the Simulation:**
   Open a web browser and navigate to:
   ```
   http://localhost:3000
   ```
   You will see the live swarm dashboard charting the boundaries, obstacles, and tracked robots.

## Expanding the Swarm

To add more robots to the simulation, open `docker-compose.yml` and duplicate one of the robot configurations, ensuring you update the container name and the `ROBOT_ID` environment variable:

```yaml
  robot-3:
    build:
      context: .
      dockerfile: Robot/Dockerfile
    environment:
      - ROBOT_ID=robot-3
    command: ["--world-engine=world-engine:50051", "--id=robot-3"]
    depends_on:
      - world-engine
    networks:
      - swarm-net
```
