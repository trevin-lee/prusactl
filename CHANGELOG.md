# Changelog

## Unreleased

First release.

- Talk to the printer directly over PrusaLink: status, files, upload and print,
  pause/resume/stop, and G-code while the printer is idle. Works over a VPN
  such as Tailscale.
- Optional Prusa Connect support with terminal sign-in: camera, on-screen
  dialogs, print queue, history, events, telemetry, and firmware commands.
- MCP server (`prusactl mcp`) whose tools pick the direct route when the
  printer answers and fall back to Connect.
- Distributed as binaries, a Homebrew cask, an MCP bundle, and an MCP Registry
  listing.
