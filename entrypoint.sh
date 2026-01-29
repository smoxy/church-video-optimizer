#!/bin/sh

# Default PUID/PGID if not specified
PUID=${PUID:-1000}
PGID=${PGID:-1000}

echo "Starting with PUID: $PUID, PGID: $PGID"

# Update group ID if needed
if ! grep -q ":$PGID:" /etc/group; then
    groupmod -o -g "$PGID" appgroup
else
    echo "Group ID $PGID already exists"
fi

# Update user ID
usermod -o -u "$PUID" appuser

# Ensure permissions on data directories
echo "Fixing permissions on /data/output and /tmp/ingest"
chown -R appuser:appgroup /data/output /tmp/ingest

# Drop privileges and execute command
exec su-exec appuser "$@"
