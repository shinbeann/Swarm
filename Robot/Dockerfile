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

EXPOSE 50052

ENTRYPOINT ["./robot-bin"]