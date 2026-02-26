FROM node:20-alpine AS ui-builder

WORKDIR /ui

COPY Visualiser/ui/package*.json ./
RUN npm install

COPY Visualiser/ui ./
RUN npm run build


FROM golang:1.25-alpine AS server-builder

WORKDIR /app

# We need Communications and Visualiser modules
COPY Communications /app/Communications
COPY Visualiser /app/Visualiser

WORKDIR /app/Visualiser/server
RUN go mod download
RUN go build -o /visualiser-bin main.go


FROM alpine:latest

WORKDIR /app

# Copy the server binary
COPY --from=server-builder /visualiser-bin /app/visualiser-bin

# Copy the built UI assets
COPY --from=ui-builder /ui/dist /app/ui/dist

# Expose combined port
EXPOSE 8080

ENTRYPOINT ["/app/visualiser-bin", "--static-dir=/app/ui/dist"]
