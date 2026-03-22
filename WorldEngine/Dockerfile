FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy all source code first
COPY Communications/ ./Communications/
COPY WorldEngine/ ./WorldEngine/

# Copy dependency files from WorldEngine
WORKDIR /app/WorldEngine
COPY WorldEngine/go.mod* ./
COPY WorldEngine/go.sum* ./

# Download dependencies
RUN go mod download

# Build the WorldEngine binary
RUN go build -o /worldengine-bin .

# Final stage
FROM alpine:latest

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /worldengine-bin .

EXPOSE 50051

CMD ["./worldengine-bin"]