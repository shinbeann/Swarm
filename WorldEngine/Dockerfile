FROM golang:1.25-alpine AS builder

WORKDIR /app

# We need to copy both the worldengine and communications modules because worldengine depends on communications
COPY Communications /app/Communications
COPY WorldEngine /app/WorldEngine

WORKDIR /app/WorldEngine

# Download dependencies and build
RUN go mod download
RUN go build -o /worldengine-bin main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /worldengine-bin /app/worldengine-bin

# Expose gRPC port
EXPOSE 50051

ENTRYPOINT ["/app/worldengine-bin"]
