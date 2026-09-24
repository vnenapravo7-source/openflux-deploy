#!/bin/bash
# Remove utun routes that may have been left by a crashed client.
set -e
echo "Removing utun routes (if any)..."
sudo route delete -net 0.0.0.0/1 2>/dev/null || true
sudo route delete -net 128.0.0.0/1 2>/dev/null || true
echo "Checking for leftover utun interfaces..."
ifconfig | grep -E "^utun[0-9]+" || echo "  (none)"
echo "Done. If a utun is still up but no process is attached, it will be"
echo "cleaned up by the OS once the last fd is closed - which already happened."
