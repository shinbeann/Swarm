FROM golang:1.25-alpine AS builder

WORKDIR /app

# We need to copy both the robot and communications modules because robot depends on communications
COPY Communications /app/Communications
COPY Robot /app/Robot

WORKDIR /app/Robot

# Download dependencies and build
RUN go mod download
RUN go build -o /robot-bin main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /robot-bin /app/robot-bin

ENTRYPOINT ["/app/robot-bin"]
