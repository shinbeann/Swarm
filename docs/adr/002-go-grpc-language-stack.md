# Robot and World Engine Implemented in Go with gRPC

## Context
The Robot and World Engine subprojects required a compiled, performant systems language for the simulation's control loops and gRPC server logic. The project's primary architectural contract (gRPC) is first-class in several languages. The project rules mandate Go 1.25.

## Decision
Both the `Robot` and `WorldEngine` subprojects are implemented in **Go**, using the standard `google.golang.org/grpc` library for all inter-service communication. The gRPC contract definitions live in the `Communications` subproject as `.proto` files, with generated Go stubs committed alongside them in `Communications/proto/`.

Each subproject has its own `go.mod` file and uses a local `replace` directive to depend on the `Communications` module:
```
replace github.com/yihre/swarm-project/communications => ../Communications
```
This allows all modules to be built in isolation or together via Docker Compose's multi-stage builds.

## Consequences
- **Easier**: Strong typing from protobuf-generated code prevents contract mismatches between the Robot and World Engine at compile time.
- **Easier**: Go's goroutine model makes running concurrent control loops (poll timers, gRPC servers, heartbeat watchers) extremely lightweight.
- **Harder**: Any change to a `.proto` file requires re-running `protoc` to regenerate the Go stubs before code that imports them will compile.
- **Overhead**: The local `replace` directive in `go.mod` means direct `go get` for external use of these modules will not work without publishing them to a module proxy.
