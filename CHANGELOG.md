# Changelog

## 0.1.3 (2026-09-28)

- When Prusa changes an API, prusactl says so plainly instead of misbehaving:
  "Prusa Connect answered in a way prusactl doesn't recognize: …", naming the
  request and what was unexpected, with what to do. It covers removed Connect
  endpoints, changed response formats and missing fields (a renamed printer
  list no longer reads as "no printers"), Prusa Account sign-in and token
  changes, and firmware changes to the printer's local API.
- A rejected sign-in app ID no longer deletes the saved session.

## 0.1.2 (2026-09-28)

- The plate check applies to every tool that starts a job or moves the printer:
  after a finished or stopped print, `send_command` HOME, MOVE, MOVE_Z,
  MESH_BED_LEVELING, and START_PRINT need `plate_clear`, like `start_print`.
  Marking the printer ready is documented as the same confirmation, and
  `api_request` as raw access that skips the checks.
- The README explains how to uninstall completely, including the saved
  password and session.

## 0.1.1 (2026-09-28)

- Windows: `setup` and `login` work over SSH and in services, where Windows
  has no Credential Manager; credentials go in the private `secrets.json`.
  Tested on Windows 11 ARM64: the test suite, setup, status, and the MCP server.
- `list_printer_files` pages the printer's folders (default 50, `next_offset`,
  at most 500), and every tool result is capped at 256 KB with a request to
  narrow it.
- `setup --forget` says "API key" when it removes one.

## 0.1.0 (2026-09-28)

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
- Credentials stay out of transcripts: API keys, tokens and passwords are
  masked in every tool result, error, and `prusactl api` response (`--raw`
  shows them).
- Runs on headless Linux such as a Raspberry Pi: with no system keychain,
  credentials go in a private `secrets.json` (`PRUSACTL_KEYRING=file` forces
  it).
- Distributed as binaries, a Homebrew cask, an MCP bundle, and an MCP Registry
  listing.
