# Releasing

## One-time setup (maintainers)

| For | Do this |
|---|---|
| Homebrew | Create the public repository `moseleydev/homebrew-tap`. Add a fine-grained token with *Contents: read and write* on it as the secret `HOMEBREW_TAP_GITHUB_TOKEN` in this repository. Without it, releases skip Homebrew. |
| npm | npm only accepts a *trusted publisher* for a package that already exists, so publish the first version by hand: `cd sdks/node && npm login && npm publish --access public`. Then, on the package's settings page on npmjs.com, add a trusted publisher for GitHub Actions: owner `moseleydev`, repository `tracelet`, workflow `publish-sdks.yml`. Push the next `sdk-node-v*` tag within 2 days, or npm expires the unused configuration. Once a CI publish has worked, choose *Require two-factor authentication and disallow tokens*. No token is stored. |
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

The workflow refuses to publish if the tag and the version in the package file disagree. The first Node SDK version is published by hand (see One-time setup), so its first tag is the version after that.
