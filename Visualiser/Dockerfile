FROM node:20-alpine AS ui-builder

WORKDIR /app

# Copy UI source
COPY Visualiser/ui ./

# Install dependencies and build
RUN npm install && npm run build

# Build Go server
FROM golang:1.25-alpine AS go-builder

WORKDIR /app

# Copy all source code
COPY . .

# Build the Visualiser binary
WORKDIR /app/Visualiser/server
RUN go build -o /visualiser-bin .

# Final runtime stage
FROM alpine:latest

WORKDIR /root/

# Copy binary from Go builder
COPY --from=go-builder /visualiser-bin .

# Copy UI dist folder from UI builder
COPY --from=ui-builder /app/dist ./ui/dist

EXPOSE 8080

ENTRYPOINT ["./visualiser-bin"]