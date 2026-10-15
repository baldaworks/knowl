# Publish a Knowl release

The release workflow is `.github/workflows/release.yml`. It runs for annotated
`vX.Y.Z` tags whose commits belong to `main`. The Go version comes from `go.mod`;
the native npm target matrix and package identity come from
`.omnidist/omnidist.yaml`.

## npm trusted publishing

Omnidist uses `publish-auth: trusted` and the repository URL
`git+https://github.com/baldaworks/knowl.git`. The publishing job has
`id-token: write` and installs npm 12.2.0. It does not require an npm token
secret.

An npm package owner must configure GitHub Actions as a trusted publisher for
each of these packages:

- `@baldaworks/knowl`
- `@baldaworks/knowl-darwin-x64`
- `@baldaworks/knowl-darwin-arm64`
- `@baldaworks/knowl-linux-x64`
- `@baldaworks/knowl-linux-arm64`
- `@baldaworks/knowl-win32-x64`

Use organization `baldaworks`, repository `knowl`, workflow filename
`release.yml`, and no environment name. Allow direct `npm publish`.

From an authenticated npm owner account, inspect the commands derived from the
checked-in Omnidist configuration:

```bash
npx -y @omnidist/omnidist@latest --profile default npm trust \
  --repo baldaworks/knowl --workflow-file release.yml
```

Add `--apply` to register the reviewed bindings. npm requires existing packages,
owner write access, and account 2FA for this setup. A previously unpublished
package needs its initial authenticated publication before its trust binding
can be registered. This one-time owner action is separate from subsequent
tokenless CI publishing. See the [npm trust prerequisites](https://docs.npmjs.com/cli/v12/commands/npm-trust/#prerequisites).

## Cut and verify a release

Align the checked-in plugin, skill contracts, release metadata and installation
guides with the intended version. Run `go run ./internal/cmd/releasecheck
--version X.Y.Z`, the repository tests and linters, and merge the reviewed changes
into `main` before creating the annotated tag.

The workflow verifies the release, stages the five native npm packages and
launcher, and publishes only after all verification jobs pass. It also publishes
the multi-platform GHCR image and the GitHub Release with its immutable image
digest. Confirm the completed workflow and actual registry versions before
reporting publication as successful.

Preserve failed-run logs and verified artifacts. Do not move an existing public
tag to another commit; use a new patch version when a source or workflow fix
must enter the release.
