#!/usr/bin/env python3
"""Build dist/prusactl.mcpb from GoReleaser's binaries and write dist/server.json
for the MCP Registry. Usage: scripts/mcpb.py VERSION (e.g. 0.1.0)."""
import hashlib, json, os, shutil, stat, subprocess, sys

version = sys.argv[1].lstrip("v")
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
dist = os.path.join(root, "dist")

# Pick the binaries out of GoReleaser's artifact list.
artifacts = json.load(open(os.path.join(dist, "artifacts.json")))
want = {"darwin": ("all", "prusactl-darwin"), "linux": ("amd64", "prusactl-linux"), "windows": ("amd64", "prusactl.exe")}
bundle = os.path.join(dist, "mcpb")
shutil.rmtree(bundle, ignore_errors=True)
os.makedirs(os.path.join(bundle, "server"))
for goos, (goarch, name) in want.items():
    matches = [a for a in artifacts if a.get("type") in ("Binary", "Universal Binary")
               and a.get("goos") == goos and a.get("goarch") == goarch]
    if len(matches) != 1:
        sys.exit(f"expected one {goos}/{goarch} binary, found {len(matches)}")
    dst = os.path.join(bundle, "server", name)
    shutil.copy2(os.path.join(root, matches[0]["path"]), dst)
    os.chmod(dst, os.stat(dst).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

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
