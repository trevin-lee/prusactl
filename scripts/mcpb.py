#!/usr/bin/env python3
"""Build dist/prusactl.mcpb from GoReleaser's binaries and write dist/server.json
for the MCP Registry. Usage: scripts/mcpb.py VERSION (e.g. 0.1.0)."""
import hashlib, json, os, shutil, stat, subprocess, sys

version = sys.argv[1].lstrip("v")
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
dist = os.path.join(root, "dist")

# Pick the binaries out of GoReleaser's artifact list.
artifacts = json.load(open(os.path.join(dist, "artifacts.json")))
# Linux ships both CPUs behind a launcher, so the bundle works on a Raspberry Pi
# too; macOS is one universal binary, and Windows on ARM runs the x86-64 one.
want = {
    ("darwin", "all"): "prusactl-darwin",
    ("linux", "amd64"): "prusactl-linux-amd64",
    ("linux", "arm64"): "prusactl-linux-arm64",
    ("windows", "amd64"): "prusactl.exe",
}
bundle = os.path.join(dist, "mcpb")
shutil.rmtree(bundle, ignore_errors=True)
os.makedirs(os.path.join(bundle, "server"))
def executable(path):
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

for (goos, goarch), name in want.items():
    matches = [a for a in artifacts if a.get("type") in ("Binary", "Universal Binary")
               and a.get("goos") == goos and a.get("goarch") == goarch]
    if len(matches) != 1:
        sys.exit(f"expected one {goos}/{goarch} binary, found {len(matches)}")
    dst = os.path.join(bundle, "server", name)
    shutil.copy2(os.path.join(root, matches[0]["path"]), dst)
    executable(dst)

# The bundle format picks a command per operating system, not per CPU.
launcher = os.path.join(bundle, "server", "prusactl-linux")
with open(launcher, "w") as f:
    f.write('''#!/bin/sh
# Runs the build for this machine's CPU.
dir=$(dirname "$0")
case $(uname -m) in
  aarch64 | arm64) exec "$dir/prusactl-linux-arm64" "$@" ;;
  x86_64 | amd64)  exec "$dir/prusactl-linux-amd64" "$@" ;;
  *) echo "prusactl: this extension has no build for $(uname -m); install prusactl with Homebrew instead" >&2; exit 1 ;;
esac
''')
executable(launcher)

manifest = json.load(open(os.path.join(root, "mcpb", "manifest.json")))
manifest["version"] = version
json.dump(manifest, open(os.path.join(bundle, "manifest.json"), "w"), indent=2)
for f in ("README.md", "LICENSE"):
    shutil.copy2(os.path.join(root, f), bundle)

out = os.path.join(dist, "prusactl.mcpb")
npx = ["npx", "-y", "@anthropic-ai/mcpb@2"]
subprocess.run(npx + ["validate", os.path.join(bundle, "manifest.json")], check=True)
subprocess.run(npx + ["pack", bundle, out], check=True)
sha = hashlib.sha256(open(out, "rb").read()).hexdigest()

server = json.load(open(os.path.join(root, "server.json")))
server["version"] = version
pkg = server["packages"][0]
pkg["identifier"] = f"https://github.com/trevin-lee/prusactl/releases/download/v{version}/prusactl.mcpb"
pkg["fileSha256"] = sha
json.dump(server, open(os.path.join(dist, "server.json"), "w"), indent=2)
print(f"{out} sha256={sha}")
