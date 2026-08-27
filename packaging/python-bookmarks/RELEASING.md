# Releasing shinyhub-bookmarks

`shinyhub-bookmarks` is versioned independently from the ShinyHub server. A
release is built from an immutable package-specific tag and published through
PyPI Trusted Publishing; no long-lived API token is used.

## First release only

Before pushing the first tag, add a pending GitHub publisher under the PyPI
account's **Publishing** settings:

- PyPI project name: `shinyhub-bookmarks`
- GitHub owner: `rvben`
- Repository: `shinyhub`
- Workflow: `publish-bookmarks.yml`
- Environment: `pypi`

The pending publisher creates the project on its first successful upload. It
does not reserve the name before then.

## Release checklist

1. Update `project.version` in `pyproject.toml` and `__version__` in
   `src/shinyhub_bookmarks/__init__.py` in the same commit.
2. Run `make test-py-bookmarks` and the repository's JavaScript test suite.
3. Build both distributions and validate them with a current Twine release.
4. Merge the release commit to `main`.
5. Tag that exact commit as `shinyhub-bookmarks-v<version>` and push the tag.
6. Wait for the **publish shinyhub-bookmarks** workflow to finish.
7. Verify the version, files, project links, and Trusted Publisher attestation
   on PyPI.

The workflow rejects a tag whose version does not exactly match the package
metadata. PyPI also rejects re-uploading an existing filename, so published
versions are immutable; issue a patch release for any correction.
