#!/usr/bin/env python3
"""Create the single draft release for a tag, and rebuild its latest.json after all builds.

Why this exists (v0.6.2): the four tauri-action jobs each "find or create" the draft.
They ran in the same second and created two drafts, each holding half the installers
and half a latest.json; every job still reported success. Separately, tauri-action
updates latest.json by read-modify-delete-upload, so two jobs finishing together can
drop each other's entries even inside one release.

So release.yml now runs:
  create    before the builds: make exactly one draft (or reuse it) and output its id;
  finalize  after the builds:  rebuild latest.json from the .sig assets in that draft,
            with releases/download/<tag>/<asset> URLs, and fail unless every platform
            the updater needs is present.

Network calls go through the `gh` CLI (present on GitHub runners): it handles the
asset download redirect, which drops the Authorization header for us.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

# Updater platform key -> suffix of the installer asset that serves it. The key set
# matches every published latest.json since v0.4 (11 entries); tauri.conf.json's
# updater reads releases/latest/download/latest.json, so a missing key means that
# platform's users silently never see the release.
PLATFORMS = [
    ("darwin-aarch64", "_aarch64.app.tar.gz"),
    ("darwin-aarch64-app", "_aarch64.app.tar.gz"),
    ("darwin-x86_64", "_x64.app.tar.gz"),
    ("darwin-x86_64-app", "_x64.app.tar.gz"),
    ("linux-x86_64", "_amd64.AppImage"),
    ("linux-x86_64-appimage", "_amd64.AppImage"),
    ("linux-x86_64-deb", "_amd64.deb"),
    ("linux-x86_64-rpm", ".x86_64.rpm"),
    # NSIS is the default Windows entry: it upgrades in place without elevation
    # (updaterJsonPreferNsis in release.yml).
    ("windows-x86_64", "_x64-setup.exe"),
    ("windows-x86_64-nsis", "_x64-setup.exe"),
    ("windows-x86_64-msi", "_x64_en-US.msi"),
]

MANIFEST = "latest.json"


class ReleaseError(Exception):
    pass


def choose_draft(releases, tag):
    """Return the id of the one draft for `tag`, None if there is none yet.

    Refuses a published release with that tag (never touch a public release) and
    more than one draft (that is exactly the broken state this script prevents;
    a human has to pick which one to keep).
    """
    matching = [r for r in releases if r.get("tag_name") == tag]
    published = [r["id"] for r in matching if not r.get("draft")]
    if published:
        raise ReleaseError(f"{tag} 已有公开的 Release（id {published}），不能再往里传构建产物")
    drafts = [r["id"] for r in matching if r.get("draft")]
    if len(drafts) > 1:
        raise ReleaseError(f"{tag} 有 {len(drafts)} 个草稿（id {drafts}），请先删到只剩一个")
    return drafts[0] if drafts else None


def build_manifest(assets, signatures, tag, repository, version, notes, pub_date):
    """Build latest.json for the draft.

    `assets` are the draft's asset names; `signatures` maps "<installer>.sig" to its
    text. Every platform must resolve to exactly one installer that has a signature.
    """
    installers = [name for name in assets if not name.endswith(".sig") and name != MANIFEST]
    base = f"https://github.com/{repository}/releases/download/{tag}/"
    platforms = {}
    problems = []
    for key, suffix in PLATFORMS:
        found = [name for name in installers if name.endswith(suffix)]
        if len(found) != 1:
            problems.append(f"{key}：以 {suffix} 结尾的安装包有 {len(found)} 个 {found}")
            continue
        signature = signatures.get(found[0] + ".sig")
        if not signature:
            problems.append(f"{key}：{found[0]} 没有签名文件")
            continue
        platforms[key] = {"signature": signature, "url": base + found[0]}
    if problems:
        raise ReleaseError("更新清单不完整：\n  " + "\n  ".join(problems))
    return {"version": version, "notes": notes, "pub_date": pub_date, "platforms": platforms}


# ---- GitHub I/O -------------------------------------------------------------


def gh(*args, data=None):
    result = subprocess.run(
        ["gh", *args], input=data, capture_output=True, check=False
    )
    if result.returncode != 0:
        raise ReleaseError(f"gh {' '.join(args)} 失败：{result.stderr.decode(errors='replace')}")
    return result.stdout


def gh_json(*args):
    return json.loads(gh("api", *args))


def list_releases(repo):
    releases = []
    for page in range(1, 6):
        batch = gh_json(f"repos/{repo}/releases?per_page=100&page={page}")
        releases.extend(batch)
        if len(batch) < 100:
            break
    return releases


def create(repo, tag, body_file, target):
    release_id = choose_draft(list_releases(repo), tag)
    if release_id is None:
        with open(body_file, encoding="utf-8") as fh:
            body = fh.read()
        payload = json.dumps(
            {"tag_name": tag, "name": tag, "body": body, "draft": True,
             "prerelease": False, "target_commitish": target}
        ).encode()
        release_id = json.loads(gh("api", "-X", "POST", f"repos/{repo}/releases", "--input", "-", data=payload))["id"]
        print(f"已创建草稿 {tag}（id {release_id}）")
    else:
        print(f"复用已有草稿 {tag}（id {release_id}）")
    # Re-check: if anything else raced us into a second draft, stop here rather
    # than letting four builds split their uploads again.
    if choose_draft(list_releases(repo), tag) != release_id:
        raise ReleaseError(f"{tag} 的草稿在创建后出现了变化，请检查 Releases 页面")
    return release_id


def finalize(repo, tag, release_id):
    release = gh_json(f"repos/{repo}/releases/{release_id}")
    if not release.get("draft") or release.get("tag_name") != tag:
        raise ReleaseError(f"Release {release_id} 不是 {tag} 的草稿")
    if choose_draft(list_releases(repo), tag) != release_id:
        raise ReleaseError(f"{tag} 不止一个草稿")
    assets = gh_json(f"repos/{repo}/releases/{release_id}/assets?per_page=100")
    by_name = {a["name"]: a for a in assets}

    def download(name):
        return gh("api", "-H", "Accept: application/octet-stream",
                  f"repos/{repo}/releases/assets/{by_name[name]['id']}")

    signatures = {name: download(name).decode() for name in by_name if name.endswith(".sig")}
    previous = json.loads(download(MANIFEST)) if MANIFEST in by_name else {}
    version = previous.get("version") or tag.lstrip("v")
    manifest = build_manifest(
        by_name, signatures, tag, repo, version,
        notes=release.get("body") or "",
        pub_date=previous.get("pub_date") or datetime.now(timezone.utc).isoformat(),
    )
    missing = sorted(set(manifest["platforms"]) - set(previous.get("platforms", {})))
    if missing:
        print(f"tauri-action 上传的清单缺了这些平台，已补上：{', '.join(missing)}")

    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, MANIFEST)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(manifest, fh, indent=2, ensure_ascii=False)
        if MANIFEST in by_name:
            gh("api", "-X", "DELETE", f"repos/{repo}/releases/assets/{by_name[MANIFEST]['id']}")
        with open(path, "rb") as fh:
            gh("api", "-X", "POST",
               f"https://uploads.github.com/repos/{repo}/releases/{release_id}/assets?name={MANIFEST}",
               "-H", "Content-Type: application/json", "--input", "-", data=fh.read())
    for key, entry in sorted(manifest["platforms"].items()):
        print(f"  {key:24} {entry['url'].rsplit('/', 1)[1]}")
    print(f"latest.json：{manifest['version']}，{len(manifest['platforms'])} 个平台")


def main(argv=None):
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    c = sub.add_parser("create")
    c.add_argument("--tag", required=True)
    c.add_argument("--body-file", required=True)
    c.add_argument("--target", required=True)
    f = sub.add_parser("finalize")
    f.add_argument("--tag", required=True)
    f.add_argument("--release-id", required=True, type=int)
    args = parser.parse_args(argv)
    repo = os.environ["GITHUB_REPOSITORY"]
    try:
        if args.command == "create":
            release_id = create(repo, args.tag, args.body_file, args.target)
            out = os.environ.get("GITHUB_OUTPUT")
            if out:
                with open(out, "a", encoding="utf-8") as fh:
                    fh.write(f"id={release_id}\n")
        else:
            finalize(repo, args.tag, args.release_id)
    except ReleaseError as error:
        print(f"::error::{error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
