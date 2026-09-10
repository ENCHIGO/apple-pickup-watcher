#!/usr/bin/env python3
"""Create release/<tag> refs for published releases; never move existing refs."""

import json
import os
import subprocess
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen


class GitHub:
    def __init__(self, repository, token, api_url="https://api.github.com"):
        self.base = f"{api_url.rstrip('/')}/repos/{repository}"
        self.token = token

    def __call__(self, method, path, data=None):
        request = Request(
            self.base + path,
            data=None if data is None else json.dumps(data).encode(),
            headers={
                "Authorization": f"Bearer {self.token}",
                "Accept": "application/vnd.github+json",
                "Content-Type": "application/json",
                "User-Agent": "apple-pickup-watcher-release-branches",
            },
            method=method,
        )
        with urlopen(request, timeout=30) as response:
            return json.load(response)


def published_tags(api, selected_tag=""):
    if selected_tag:
        release = api("GET", f"/releases/tags/{quote(selected_tag, safe='')}")
        if release["draft"]:
            raise ValueError(f"Release is still a draft: {selected_tag}")
        return [release["tag_name"]]
    tags = []
    page = 1
    while True:
        releases = api("GET", f"/releases?per_page=100&page={page}")
        tags.extend(release["tag_name"] for release in releases if not release["draft"])
        if len(releases) < 100:
            return tags
        page += 1


def tag_commit(api, tag):
    obj = api("GET", f"/git/ref/tags/{quote(tag, safe='/')}")["object"]
    seen = set()
    while obj["type"] == "tag":
        if obj["sha"] in seen or len(seen) >= 16:
            raise ValueError(f"Invalid tag chain: {tag}")
        seen.add(obj["sha"])
        obj = api("GET", f"/git/tags/{obj['sha']}")["object"]
    if obj["type"] != "commit":
        raise ValueError(f"Tag does not resolve to a commit: {tag}")
    return obj["sha"]


def get_branch(api, branch):
    try:
        return api("GET", f"/git/ref/heads/{quote(branch, safe='/')}")
    except HTTPError as error:
        if error.code != 404:
            raise
        return None


def ensure_branch(api, tag):
    branch = f"release/{tag}"
    subprocess.run(["git", "check-ref-format", f"refs/heads/{branch}"], check=True)
    commit = tag_commit(api, tag)
    existing = get_branch(api, branch)
    if existing is None:
        try:
            existing = api("POST", "/git/refs", {"ref": f"refs/heads/{branch}", "sha": commit})
            result = "created"
        except HTTPError as error:
            # Another run or maintainer may have created it after our GET.
            if error.code != 422:
                raise
            existing = get_branch(api, branch)
            if existing is None:
                raise
            result = "preserved"
    else:
        result = "preserved"
    return {
        "branch": branch,
        "tag_commit": commit,
        "branch_commit": existing["object"]["sha"],
        "result": result,
    }


def main():
    api = GitHub(
        os.environ["GITHUB_REPOSITORY"],
        os.environ["GH_TOKEN"],
        os.environ.get("GITHUB_API_URL", "https://api.github.com"),
    )
    for tag in published_tags(api, os.environ.get("RELEASE_TAG", "")):
        print(json.dumps(ensure_branch(api, tag)), flush=True)


if __name__ == "__main__":
    main()
