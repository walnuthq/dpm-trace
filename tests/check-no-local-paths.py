import sys
import subprocess
from pathlib import Path


SKIP_DIRS = {
    ".git",
    ".venv",
    ".dpm-home",
    ".pytest_cache",
    "__pycache__",
    ".lit",
}

FORBIDDEN = [
    "/" + "Users" + "/",
    "dj" + "todorovic",
    "Mac" + "Book",
    "/" + "private" + "/",
    "/" + "var" + "/" + "folders" + "/",
]


def should_skip(path: Path) -> bool:
    return any(part in SKIP_DIRS for part in path.parts)


def candidate_files(root: Path) -> list[Path]:
    result = subprocess.run(
        [
            "git",
            "-C",
            str(root),
            "ls-files",
            "--cached",
            "--others",
            "--exclude-standard",
            "-z",
        ],
        check=True,
        capture_output=True,
    )
    return [
        root / item.decode("utf-8")
        for item in result.stdout.split(b"\0")
        if item
    ]


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-no-local-paths.py <repo-root>", file=sys.stderr)
        return 2

    root = Path(sys.argv[1]).resolve()
    failures = []

    for path in sorted(candidate_files(root)):
        rel = path.relative_to(root)
        if should_skip(rel) or not path.is_file():
            continue

        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError as exc:
            failures.append(f"{rel}: could not read: {exc}")
            continue

        for line_no, line in enumerate(text.splitlines(), 1):
            if any(marker in line for marker in FORBIDDEN):
                failures.append(f"{rel}:{line_no}: contains local machine path/user marker")

    if failures:
        print("local path leak check failed:")
        print("\n".join(failures))
        return 1

    print("local path leak check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
