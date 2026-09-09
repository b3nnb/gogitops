#!/bin/bash
# Check disk usage on root partition
# Output: "OK: disk at XX%" or "WARN: disk at XX%"
usage=$(df / | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
if [ "$usage" -gt 80 ] 2>/dev/null; then
    echo "WARN: disk at ${usage}%"
else
    echo "OK: disk at ${usage}%"
fi
