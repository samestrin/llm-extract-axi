# Release checklist

Steps for cutting a release of `llm-extract`. Run steps 1 to 4 from the repository root on the commit that will be tagged; steps 5 and 6 follow the published release.

Two of them keep the two recorded numbers honest: the demo GIF and the performance table. Both show what the release binary does. The GIF is re-rendered when the release changes something it shows; the table is re-timed on every release, so its version line always names one.

## 1. Green before a tag

```bash
make lint
make test-race
make cover-gate
```

CI runs the same checks on `main`; a release is never built from a commit whose tests fail.

## 2. Re-render the demo when a command it shows changes

`docs/demo/llm-extract.gif` records `llm-extract --version`, `llm-extract extract-links --axi` and `llm-extract extract-links --fields href,score --plain`. Re-render it when any of those commands, their output, or the fixture they run against (`benchmarks/fixtures/docs-page.html`) changed since the GIF was last rendered, and when the version line in the GIF would name an older release.

```bash
make build
vhs docs/demo/llm-extract.tape
```

- [ ] `vhs` is installed with `ttyd` and `ffmpeg` (see `docs/demo/README.md`).
- [ ] The render finishes and the session is under 30 seconds.
- [ ] Every command in it succeeds; no error text in the recording.
- [ ] `docs/demo/llm-extract.gif` is committed with the tape change.
- [ ] No hosted recording link (`vhs.charm.sh`) was added anywhere.

The tape's commands are replayed without `vhs` by `go test ./tests/e2e`, so a command that stopped working fails CI before the render is needed.

## 3. CHANGELOG

- [ ] `CHANGELOG.md` has a `[Unreleased]` section that lists every user-visible change since the last tag, in plain words, without ticket IDs.
- [ ] That section is renamed to the version and today's date when the tag is pushed.

## 4. Tag and publish

```bash
git tag v<version>
git push origin v<version>
```

The `release` workflow tests the tagged commit, builds `llm-extract` and `llm-extract-mcp` for every platform, writes `SHA256SUMS.txt`, and publishes them as the release. Check the run is green before announcing anything.

- [ ] The release workflow run for the tag is green.
- [ ] The release page lists both binaries per platform plus `SHA256SUMS.txt`.
- [ ] The binaries report the tagged version (`llm-extract --version`).

## 5. Check the install path

- [ ] On a machine without a checkout, the install one-liner installs the new version from the new release:

      ```bash
      curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash
      llm-extract --version
      ```

- [ ] A checksum mismatch or a failed download still installs nothing (the installer's own tests cover this, but the published assets are what it reads).

## 6. Re-time the released binary

The `## Performance` table in the README is generated, never typed. It is re-run after the tag, against the binary the release published, so its Version line names the release. Run before the tag, `perf.sh` stamps a `git describe` version such as `1.0.1-23-g8f6fbb7`, which names no release.

Download the release binary for the machine that will be named in the table, then:

```bash
./benchmarks/perf.sh -bin path/to/llm-extract-<os>-<arch>
```

- [ ] The table's **Version** line reads `llm-extract <version>`, the tag just pushed, with no `-N-g<hash>` or `-dirty` suffix.
- [ ] `benchmarks/results-performance.json` and `benchmarks/results-performance.md` are rewritten, not hand-edited.
- [ ] The table names the version, the date, the machine and the method.
- [ ] Every figure in the README's `## Performance` table matches the generated table.
- [ ] Both files and the README are committed in a pull request after the tag.
