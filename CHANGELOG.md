# Changelog

## 0.1.6 (2026-09-28)

- `connection_status` no longer hands the agent the whole Prusa Account record
  (email address, account id, terms dates, team organization ids). It returns
  who is signed in and the default team, as its description always promised.
- The MCP bundle works on ARM Linux, such as a Raspberry Pi; it previously
  installed and then failed to start there.
- Every list pages the same way: one cap (500), shared field descriptions, and
  `next_offset` on Connect-backed lists too. `list_jobs` with a huge limit now
  returns a page instead of "result too large".
- `get_queue`, `add_to_queue` and `remove_from_queue` answer in one shape, and
  `get_queue` no longer reports a route it can't vary.
- The README discloses that `run_gcode` leaves `/usb/prusactl-macro.gcode` on
  the printer, lists `prusactl version`, and qualifies "no browser involved".

## 0.1.5 (2026-09-28)

- The MCP bundle no longer tells the agent Prusa Connect is unavailable there.
  It reads the same saved session, so the camera, queue and history work
  whenever an installed prusactl has signed in; only the sign-in needs a
  terminal.
- `control_print` takes pause, resume, or stop. "continue", an undocumented
  second name for resume, is gone, and a bad action says so before the printer
  is asked anything.
- `get_queue` takes `limit` and `offset` like the other lists.
- `api_request` reports which route it used, and the docs no longer claim every
  tool does.
- `get_printer` describes how the two routes name their fields differently, so
  a result from one route isn't read as if it came from the other.
- `upload_file` and `list_connect_files` say that uploading through Connect
  leaves a copy in the team's storage, which only the Connect web app deletes.
- Smaller: identical folder paths from both routes, `download_printer_file`
  explains it needs the direct connection, and the sign-in prompt names the
  host the password is actually sent to.

## 0.1.4 (2026-09-28)

- Homebrew installs from source, so `brew` no longer prints a deprecation
  warning on every command. Coming from 0.1.3 or earlier, switch once with
  `brew uninstall --cask prusactl && brew update && brew install trevin-lee/tap/prusactl`.
- Running `setup` again removes the password it replaces, so a changed printer
  address no longer leaves credentials behind, and `--forget` clears everything.
- `via` is honoured everywhere: a tool that only works one way now refuses the
  other route instead of ignoring it, and a bad value is always an error.
- `list_printer_files` pages the same way over Prusa Connect as directly.
- `prusactl status` shows the printer's state through Connect when the direct
  route isn't set up.
- `prusactl api` parses flags like every other command (`--foo` was sent to the
  printer as the HTTP method), and checks the HTTP method.
- The MCP bundle's messages point at its own settings instead of terminal
  commands that don't apply there.
- `prusactl logout` no longer claims to remove a session that wasn't saved, and
  the version prints the same way however prusactl was built.
- The README and help describe what the CLI actually does, mark the examples
  that need Prusa Connect, and cover uninstalling and the one-printer limit.

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
