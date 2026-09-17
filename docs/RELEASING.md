# Releasing

One script per platform, each run on that platform. Every one stamps the
version from `git describe --tags` (or `--version`), checks the notices are
current, and refuses to write a package missing the Qt platform plugin, the
SVG image plugin, the command-line tools or the licences.

| Platform | Command | Produces |
|---|---|---|
| Linux | `./scripts/bundle-linux.sh` | `dist/OmegaCat-<v>-x86_64.AppImage`, `dist/omegacat-cli-<v>-linux-amd64.tar.gz` |
| macOS | `./scripts/bundle-macos.sh` | `dist/OmegaCat-<v>-macos-arm64.dmg`, `dist/omegacat-cli-<v>-darwin-arm64.tar.gz` |
| Windows | `scripts\bundle-windows.bat --zip` | `dist\omegacat-<v>-windows-x64.zip` |

## Steps

1. Tag: `git tag v0.3.0 && git push --tags`. An untagged build is named after
   the commit, and a dirty tree after the commit with `-dirty`.
2. Run the script on each platform from a clean checkout of the tag.
3. Attach everything in `dist/` to the GitHub release.

## Per platform

**Linux.** Build on the oldest distribution you support: an AppImage bundles
Qt but not glibc, and runs only where glibc is at least as new as what it was
built against. The script prints the floor it measured (Ubuntu 24.04 gives
GLIBC 2.38). For Ubuntu 22.04 and newer, build on 22.04 with an aqtinstall Qt
(`--qt ~/Qt/6.8.3/gcc_64`). linuxdeploy and its Qt plugin are fetched into
`build/tools` on first use.

**macOS.** Builds for the Mac's own architecture. The app is signed ad hoc,
not notarized, so a downloaded copy is quarantined; the DMG's
`READ ME FIRST.txt` gives the `xattr` command. With an Apple Developer ID, set
`OMEGACAT_CODESIGN_IDENTITY` and notarize the DMG with `xcrun notarytool`.

**Windows.** From an *x64 Native Tools Command Prompt for VS 2022* with
mingw-w64 gcc on PATH (see the script header for why both). `omegacat.exe` is
a GUI program with no console, so check its version in Help > About; the script
checks `capture.exe` and `ocvault.exe` itself.
