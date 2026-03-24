FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy all source code
COPY . .

# Build the Robot binary
WORKDIR /app/Robot
RUN go build -o /robot-bin .

# Final stage
FROM alpine:latest

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /robot-bin .

# 50052 — PeerService gRPC (gossip knowledge dissemination)
# 50053 — RaftService gRPC (leader election and log replication)
# 8081  — HTTP status endpoint (GET /status)
EXPOSE 50052 50053 8081

ENTRYPOINT ["./robot-bin"]