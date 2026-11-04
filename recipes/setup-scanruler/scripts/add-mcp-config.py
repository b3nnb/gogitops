#!/usr/bin/env python3
"""Add scanruler MCP server to Hermes config.yaml if not already present."""
import sys
import pathlib

try:
    import yaml
except ImportError:
    print("PyYAML not installed — skipping MCP config")
    sys.exit(0)

config_path = pathlib.Path.home() / ".hermes" / "config.yaml"
if not config_path.exists():
    print("No Hermes config found — skipping")
    sys.exit(0)

cfg = yaml.safe_load(config_path.read_text())
if cfg is None:
    cfg = {}

if "mcp_servers" not in cfg or cfg["mcp_servers"] is None:
    cfg["mcp_servers"] = {}

if "scanruler" in cfg["mcp_servers"]:
    print("already-configured")
    sys.exit(0)

cfg["mcp_servers"]["scanruler"] = {
    "command": "npx",
    "args": ["-y", "scanruler-mcp@latest"],
}

config_path.write_text(yaml.dump(cfg, default_flow_style=False, sort_keys=False))
print("added")