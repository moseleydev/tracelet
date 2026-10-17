# Releasing

## One-time setup (maintainers)

| For | Do this |
|---|---|
| Homebrew | Create the public repository `moseleydev/homebrew-tap`. Add a fine-grained token with *Contents: read and write* on it as the secret `HOMEBREW_TAP_GITHUB_TOKEN` in this repository. Without it, releases skip Homebrew. |
| npm | Create an npm account, then add an automation token as the secret `NPM_TOKEN`. The package name is `tracelet-sdk`. |
| PyPI | On PyPI, add a *trusted publisher* for project `tracelet-sdk`: owner `moseleydev`, repository `tracelet`, workflow `publish-sdks.yml`, environment `pypi`. Create an environment named `pypi` in this repository's settings. No token is stored. |
| Vulnerability reports | In the repository settings, turn on *Private vulnerability reporting* (SECURITY.md points people there). |

## Release Tracelet

1. Make sure `main` is green, including the Benchmark workflow.
2. Tag and push:

   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```

3. The Release workflow:
   - checks dependencies with `govulncheck`;
   - builds Studio and binaries for macOS, Linux and Windows (amd64 and arm64);
   - creates a **draft** GitHub release with checksums;
   - publishes the Homebrew cask, if that's set up.
4. Review the draft's notes and publish it. The install script picks up the newest published release.

Try a release locally without publishing anything:

```bash
goreleaser release --snapshot --clean   # artifacts in dist/
```

## Release an SDK

Bump the version in `sdks/node/package.json` or `sdks/python/pyproject.toml`, merge, then push a matching tag:

```bash
git tag sdk-node-v0.1.0 && git push origin sdk-node-v0.1.0
git tag sdk-python-v0.1.0 && git push origin sdk-python-v0.1.0
```

The workflow refuses to publish if the tag and the version in the package file disagree.
