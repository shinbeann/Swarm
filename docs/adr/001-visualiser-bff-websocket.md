# Visualiser Uses Backend-for-Frontend (BFF) with WebSockets

## Context
The Visualiser subproject must display live simulation data from the World Engine in a web browser. Standard browser JavaScript cannot initiate native gRPC connections. Two options were considered:
1. Add WebSocket or gRPC-Web support directly to the World Engine.
2. Introduce a dedicated proxy layer on the Visualiser side (BFF pattern).

The core design principle of keeping the World Engine focused purely on simulation — not on browser-specific transport concerns — motivated this decision.

## Decision
The Visualiser is implemented using a **Backend-for-Frontend (BFF)** architecture. It consists of two distinct parts:
- A **Go proxy server** that acts as a gRPC client to the World Engine, polls `GetEnvironmentData` and `GetRobotData` at a 5 Hz rate, and re-broadcasts state as JSON payloads over a WebSocket connection.
- A **React (Vite/TypeScript) web UI** that connects to the Go proxy's WebSocket, parses the JSON, and renders the simulation using **PixiJS** (WebGL-backed 2D renderer).

This keeps the World Engine's gRPC interface clean and protocol-agnostic, and isolates all web-browser-specific logic to the Visualiser subproject.

## Consequences
- **Easier**: The World Engine server code remains simple Go gRPC with no browser compatibility concerns. Any other non-browser gRPC client can easily subscribe to the same services.
- **Easier**: The React frontend can be developed and hot-reloaded independently using Vite's dev server, pointing its WebSocket at `localhost:8080`.
- **Harder**: The Visualiser has two build stages (Node/Vite for the UI, Go for the proxy), requiring a multi-stage Dockerfile and slightly more build complexity.
- **Overhead**: A polling loop at 5 Hz means the Go proxy makes gRPC calls to the World Engine every 200ms. This is a simple approach but could become a bottleneck at very high robot counts. A streaming gRPC approach could be adopted later.
