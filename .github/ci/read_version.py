import os
import pathlib
import sys


def main() -> int:
    version_file = pathlib.Path("version.md")
    if not version_file.is_file():
        print("version.md not found", file=sys.stderr)
        return 1

    lines = version_file.read_text(encoding="utf-8", errors="replace").splitlines()
    tag = (lines[0].strip() if lines else "")
    if not tag:
        print("version.md is empty", file=sys.stderr)
        return 1

    github_output = os.environ.get("GITHUB_OUTPUT")
    if not github_output:
        print("GITHUB_OUTPUT is not set", file=sys.stderr)
        return 1

    with open(github_output, "a", encoding="utf-8") as f:
        f.write(f"tag={tag}\n")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
