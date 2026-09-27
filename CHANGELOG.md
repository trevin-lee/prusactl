# Changelog

## Unreleased

First release.

- Talk to the printer directly over PrusaLink: status, files, upload and print,
  pause/resume/stop, and G-code while the printer is idle. Works over a VPN
  such as Tailscale.
- Download files from the printer's storage (`prusactl download`,
  `download_printer_file`), for example to read the slicer settings embedded
  in a finished print.
- Optional Prusa Connect support with terminal sign-in: camera, on-screen
  dialogs, print queue, history, events, telemetry, and firmware commands.
- MCP server (`prusactl mcp`) whose tools pick the direct route when the
  printer answers and fall back to Connect.
- Distributed as binaries, a Homebrew cask, an MCP bundle, and an MCP Registry
  listing.
