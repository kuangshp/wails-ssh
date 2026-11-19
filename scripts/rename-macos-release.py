"""更新已有 Release 的 Mac 文件名、中文标签及校验清单，保留原始文件内容。"""

import json
import os
from pathlib import Path
import subprocess
import tempfile


def gh(*args):
    return subprocess.check_output(["gh", *args], text=True)


tag = os.environ["GITHUB_REF_NAME"].removeprefix("asset-names-")
repository = os.environ["GITHUB_REPOSITORY"]
endpoint = f"repos/{repository}/releases/tags/{tag}"
release = json.loads(gh("api", endpoint))
checksum = gh("release", "download", tag, "--pattern", "SHA256SUMS.txt", "--output", "-")
entries = dict(line.split("  ")[::-1] for line in checksum.splitlines())
changes = []
for architecture, variant, chip in [
    ("arm64", "Apple-M-series", "苹果 M 系列芯片"),
    ("amd64", "Intel", "Intel 芯片"),
]:
    for extension, kind in [("dmg", "DMG 安装包"), ("zip", "ZIP 压缩包")]:
        old = f"wails-ssh-macos-{architecture}.{extension}"
        new = f"wails-ssh-macos-{variant}.{extension}"
        matches = [asset for asset in release["assets"] if asset["name"] in (old, new)]
        assert len(matches) == 1, (old, new)
        asset = matches[0]
        digest = entries.get(old, entries.get(new))
        assert digest and asset["digest"] == f"sha256:{digest}", old
        changes.append((asset["id"], new, f"macOS {chip} · {kind}", asset["digest"]))
        entries.pop(old, None)
        entries[new] = digest

checksum = "".join(f"{digest}  {name}\n" for name, digest in entries.items())

for asset_id, name, label, _ in changes:
    gh("api", "--method", "PATCH", f"repos/{repository}/releases/assets/{asset_id}",
       "-f", f"name={name}", "-f", f"label={label}")

with tempfile.TemporaryDirectory() as directory:
    filename = Path(directory) / "SHA256SUMS.txt"
    filename.write_text(checksum)
    gh("release", "upload", tag, str(filename), "--clobber")
gh("release", "edit", tag, "--notes-file", "docs/release-notes.md")

published = json.loads(gh("api", endpoint))
assets = {asset["id"]: asset for asset in published["assets"]}
for asset_id, name, label, digest in changes:
    assert assets[asset_id]["name"] == name
    assert assets[asset_id]["label"] == label
    assert assets[asset_id]["digest"] == digest
assert gh("release", "download", tag, "--pattern", "SHA256SUMS.txt", "--output", "-") == checksum
print("Verified Mac download names, Chinese labels and unchanged package digests.")
