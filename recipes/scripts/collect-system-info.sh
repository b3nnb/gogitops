#!/bin/bash
# Example: Script that returns info as attributes
# Collects system info and outputs key=value pairs on stdout.
# The recipe can use set_attr or parse to capture these.
# Args: none
# Output format: KEY=VALUE (one per line)
# Exit 0 = success

echo "kernel=$(uname -r)"
echo "uptime=$(uptime -p 2>/dev/null | sed 's/up //')"
echo "cpu_count=$(nproc 2>/dev/null || echo unknown)"
echo "mem_total=$(free -h 2>/dev/null | awk '/^Mem:/ {print $2}' || echo unknown)"
echo "disk_total=$(df -h / 2>/dev/null | awk 'NR==2 {print $2}' || echo unknown)"
echo "docker_version=$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || echo 'not installed')"
echo "gpu=$(lspci 2>/dev/null | grep -i 'vga\|3d\|display' | head -1 | sed 's/.*: //' || echo unknown)"
