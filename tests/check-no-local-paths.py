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

# The marker list lives in a separate file so the literals can appear whole
# (the scanner skips this file). Add your own username / home paths there.
MARKERS_FILE = Path("tests/forbidden-markers.txt")


def load_markers(repo_root: Path) -> list[str]:
    src = repo_root / MARKERS_FILE
    if not src.is_file():
        # Fall back to a minimal built-in set if the markers file is absent.
        return ["/Users/", "/private/", "/var/folders/"]
    markers: list[str] = []
    for line in src.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        markers.append(line)
    return markers


def should_skip(path: Path) -> bool:
    return any(part in SKIP_DIRS for part in path.parts)


def candidate_files(root: Path) -> list[Path]:
    result = subprocess.run(
        [
            "git",
            "-c",
            "safe.directory=*",
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
    forbidden = load_markers(root)
    # Never scan the markers file or this scanner itself: both legitimately
    # reference the marker literals. The markers file is the source of truth.
    skip_files = {
        root / MARKERS_FILE,
        root / "tests" / "check-no-local-paths.py",
    }
    failures = []

    for path in sorted(candidate_files(root)):
        rel = path.relative_to(root)
        if should_skip(rel) or not path.is_file() or path in skip_files:
            continue

        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError as exc:
            failures.append(f"{rel}: could not read: {exc}")
            continue

        for line_no, line in enumerate(text.splitlines(), 1):
            if any(marker in line for marker in forbidden):
                failures.append(f"{rel}:{line_no}: contains local machine path/user marker")

    if failures:
        print("local path leak check failed:")
        print("\n".join(failures))
        return 1

    print("local path leak check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
