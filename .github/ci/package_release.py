import pathlib
import shutil
import sys
import zipfile


def main() -> int:
    out_dir = pathlib.Path("release_out")
    pkg_dir = out_dir / "pkg"
    pkg_dir.mkdir(parents=True, exist_ok=True)

    src = pathlib.Path("dist/linux-amd64/app/V2bX")
    if not src.is_file():
        print(f"missing binary: {src}", file=sys.stderr)
        return 1

    shutil.copy2(src, pkg_dir / "V2bX")

    required = [
        "geoip.dat",
        "geosite.dat",
        "config.json",
        "dns.json",
        "route.json",
        "custom_outbound.json",
        "custom_inbound.json",
    ]

    for name in required:
        p = pathlib.Path("example") / name
        if not p.is_file():
            print(f"missing file: {p}", file=sys.stderr)
            return 1
        shutil.copy2(p, pkg_dir / name)

    out_zip = out_dir / "V2bX-linux-64.zip"
    with zipfile.ZipFile(out_zip, "w", compression=zipfile.ZIP_DEFLATED) as z:
        for p in sorted(pkg_dir.iterdir()):
            if p.is_file():
                z.write(p, arcname=p.name)

    print("created", str(out_zip))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
