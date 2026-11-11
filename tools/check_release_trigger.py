"""Decide whether this Actions run may enter the immutable release job."""
import json
import os
import subprocess
from pathlib import Path


def version_at(commit, *, allow_missing=False):
    # Distinguish an absent VERSION from unavailable Git history.
    subprocess.run(["git", "cat-file", "-e", commit + "^{commit}"], check=True)
    entry = subprocess.check_output(["git", "ls-tree", commit, "--", "VERSION"])
    if allow_missing and not entry:
        return None
    return subprocess.check_output(["git", "show", commit + ":VERSION"], encoding="utf-8").strip()


def should_publish(event_name, event, sha):
    if event_name == "workflow_dispatch":
        value = event.get("inputs", {}).get("publish", False)
        return value is True or value == "true"
    if event_name != "push":
        return False
    before = event.get("before")
    if not before:
        raise ValueError("Push event is missing its before commit")
    current = version_at(sha)
    previous = None if before == "0" * 40 else version_at(before, allow_missing=True)
    return current != previous


if __name__ == "__main__":
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text(encoding="utf-8"))
    publish = should_publish(os.environ["GITHUB_EVENT_NAME"], event, os.environ["GITHUB_SHA"])
    with Path(os.environ["GITHUB_OUTPUT"]).open("a", encoding="utf-8") as output:
        output.write("publish=" + str(publish).lower() + "\n")
