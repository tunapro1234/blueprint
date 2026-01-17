from __future__ import annotations

import re
from pathlib import Path


def test_no_gemini_below_3_in_repo():
    root = Path(__file__).resolve().parents[1]
    this_file = Path(__file__).resolve()

    exclude_dirs = {
        ".git",
        ".venv",
        "venv",
        "__pycache__",
        ".pytest_cache",
        "node_modules",
        ".blueprint",
        "blueprint",
        "dist",
        "build",
        ".idea",
        ".vscode",
    }
    exclude_suffixes = {
        ".png",
        ".jpg",
        ".jpeg",
        ".gif",
        ".pdf",
        ".zip",
        ".tar",
        ".gz",
        ".pyc",
        ".lock",
    }

    pattern = re.compile(r"gemini\s*[-_]?\s*(1(\.\d+)?|2(\.\d+)?)", re.IGNORECASE)
    matches: list[str] = []

    for path in root.rglob("*"):
        if path.is_dir():
            if path.name in exclude_dirs:
                continue
            continue

        if path == this_file:
            continue

        if any(part in exclude_dirs for part in path.parts):
            continue

        if path.suffix.lower() in exclude_suffixes:
            continue

        try:
            content = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue

        if pattern.search(content):
            matches.append(str(path.relative_to(root)))

    assert not matches, f"Gemini 3 altinda model referansi bulundu: {matches}"
