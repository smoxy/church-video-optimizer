# Build Stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod ./
# COPY go.sum ./ 
# (No dependencies currently, but good practice to have this line commented if needed later)

RUN go mod download

COPY . .

# Build the binary
# CGO_ENABLED=0 creates a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o video-optimizer ./cmd/optimizer

# Runtime Stage
FROM alpine:latest

# Install FFmpeg, shadow (for usermod), su-exec (for dropping privileges)
RUN apk add --no-cache ffmpeg ca-certificates tzdata shadow su-exec

# Create a non-root user (we will modify UID/GID in entrypoint)
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Create data directories
RUN mkdir -p /data/output /tmp/ingest

WORKDIR /app

COPY --from=builder /app/video-optimizer .
COPY entrypoint.sh .
RUN chmod +x entrypoint.sh

# We run as root initially to allow entrypoint to change UID/GID, then drop to appuser
# USER appuser <--- REMOVED

# Expose generic volume
VOLUME ["/data/output"]

ENTRYPOINT ["./entrypoint.sh"]
CMD ["./video-optimizer"]
