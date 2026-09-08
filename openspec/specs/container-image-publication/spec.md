# container-image-publication Specification

## Purpose
TBD - created by archiving change publish-container-image. Update Purpose after archive.
## Requirements
### Requirement: Released Versions Are Published As Container Images

The repository SHALL publish its container image to a registry, under a name derived from the repository (`ghcr.io/<owner>/<repository>`, lowercased as the registry requires), so that the image can be obtained without a Go toolchain, without a working tree, and without building it.

**Automatic** publication SHALL be triggered by a release actually being created, and SHALL NOT be triggered by a merge to `main`. The only other way an image is published SHALL be the deliberate, operator-initiated path defined in "An Existing Tag Can Be Published Deliberately"; there SHALL be no third trigger.

Under either trigger the version the image carries SHALL be the version named by a git tag that already exists, and the image tag and that git tag SHALL denote the same commit. The repository has exactly one authority on what a version is (`development-workflow`'s "Automated Release Pull Request"); no publication path SHALL compute a version of its own, because a second authority would be able to disagree with the first.

Publication SHALL authenticate with the workflow's own repository-scoped token. No registry account, credential, or secret beyond that token SHALL be required.

Publishing an image SHALL NOT deploy it, and no requirement here SHALL be read as describing a deployment: this repository has no target environment, and the artifact is the whole of what is delivered.

#### Scenario: A release publishes an image

- **WHEN** a release is created for version `X.Y.Z`
- **THEN** an image is published whose contents are built from the commit that release names

#### Scenario: A merge alone publishes nothing

- **WHEN** a commit merges to `main` without creating a release
- **THEN** no image is published

#### Scenario: Publication needs no credential of its own

- **WHEN** the publish runs
- **THEN** it authenticates with the workflow's own repository-scoped token, and the repository holds no registry username, password, or personal access token as a secret

### Requirement: The Published Tag Set Is The Version And `latest`

A publication SHALL push the version being published. It SHALL additionally move `latest` onto that image **only when no higher version has already been published**. Additional moving pointers (a major-only or major-minor tag) SHALL NOT be published.

`latest` therefore tracks the highest published version, not the most recent publication. The two differ exactly where the deliberate path below is used to republish an older tag: republishing `4.0.0` after `4.1.0` exists must not hand an unversioned puller an older image than the one they had yesterday, and a rule phrased as "the most recent publication" would require precisely that. This is the one place where "push exactly two tags every time" is wrong, and it is not a special case in the workflow so much as the definition of what `latest` means.

The version tag SHALL be immutable: once a version has been published, **no** publication path SHALL overwrite it, and the workflow SHALL offer no input, flag, or parameter that permits overwriting one. Replacing a published version SHALL require deleting it from the registry first, which is a deliberate act performed by a human outside this pipeline and is therefore not something a mistyped input can do.

An earlier draft of this requirement paired the word "immutable" with an opt-in that let the same operator overwrite a version by setting a boolean. That is guarded mutability, and a consumer cannot rely on it: what a version tag means has to be the same whether or not somebody set a flag.

The image tag SHALL be the version **without** any leading `v`, while the git tag retains the project's existing `vX.Y.Z` form: `git tag v4.0.0` publishes image tag `4.0.0`. Every publication path SHALL apply that same normalization before it checks whether a version exists and before it pushes, so the automatic and deliberate paths cannot check one tag and push another.

The distinction between the two tags is what makes either useful. The version tag is the one a reader can be told to pull in order to run a known artifact; if it can be rewritten, it names nothing. `latest` exists for a reader who has no version in hand, and is documented as a convenience rather than as a reproducible reference.

#### Scenario: Publishing the newest version moves `latest`

- **WHEN** version `X.Y.Z` is published and no higher version has been published before
- **THEN** both `X.Y.Z` and `latest` resolve to that image

#### Scenario: Republishing an older version leaves `latest` alone

- **WHEN** version `X.Y.Z` is published while a higher version already has a published image
- **THEN** `X.Y.Z` resolves to the newly published image and `latest` still resolves to the higher version's image

#### Scenario: A published version is not overwritten

- **WHEN** a publication is attempted for a version that already has a published image
- **THEN** it fails without pushing, and no input to the workflow can make it push instead

#### Scenario: The git tag and the image tag differ by the leading `v`

- **WHEN** git tag `vX.Y.Z` is published by either path
- **THEN** the image tag is `X.Y.Z`, and the existence check that guards the previous scenario is performed against that same normalized tag

#### Scenario: No moving major or minor pointer exists

- **WHEN** the published tags are listed after a release
- **THEN** they are the version and `latest`, and no major-only or major-minor tag is present

### Requirement: An Existing Tag Can Be Published Deliberately

The publish workflow SHALL provide a manually triggered path that publishes a git tag which already exists, building that tag's tree rather than the current state of `main`.

Without it, the first image would appear only when the next version-bumping commit lands, so documentation naming a pull command would be false from the moment it is written until some unrelated change ships. It is also the recovery path for a release whose publication failed before pushing anything — and because it takes a tag name as free text, it is precisely where a released version would be overwritten by a typo, which is why the immutability requirement above admits no override.

A tag predating this change carries a `Dockerfile` that predates it too, so building such a tag for a non-native platform emulates the Go toolchain rather than cross-compiling it. That SHALL be accepted rather than worked around: the resulting binaries are correct for their platform and only the build is slow, and rewriting a released tag's tree to obtain a faster build would defeat the point of building the tag at all. The cross-compilation requirement in `container-image` governs what the repository builds from now on, not what a historical tag contained.

The tag it is given SHALL be validated before anything is built or pushed: it SHALL match the project's release-tag form, and it SHALL resolve to a tag that actually exists in the repository. An input that is free text and reaches a build unvalidated turns a typo into either a confusing build failure minutes later or, worse, a published image under a tag nobody intended — and the normalization that derives the image tag from it only makes sense over a value already known to have that shape.

#### Scenario: A malformed or unknown tag is refused before any build

- **WHEN** the manual path is triggered with a value that does not match the release-tag form, or that names a tag which does not exist in the repository
- **THEN** it fails immediately, before any image is built and before anything is pushed

#### Scenario: An already-released version is published after the fact

- **WHEN** the manual path is triggered for an existing git tag that has no published image
- **THEN** an image is published from that tag's tree, tagged with that version

#### Scenario: The manual path builds the tag, not the branch

- **WHEN** the manual path publishes tag `vX.Y.Z` while `main` has advanced beyond it
- **THEN** the image contents are those of `vX.Y.Z`, not of `main`

#### Scenario: The manual path refuses a version that already has an image

- **WHEN** the manual path is triggered for a version whose image is already published
- **THEN** it fails without pushing

#### Scenario: A pre-change tag is built as it was

- **WHEN** the manual path publishes a tag whose tree predates the multi-platform build
- **THEN** the publication succeeds with correct per-platform binaries, and the slower emulated build is accepted rather than treated as a defect

### Requirement: The Published Image Is Pullable Without Credentials

The published image SHALL be obtainable by a client that has not authenticated to the registry.

This SHALL NOT be assumed, in either direction, from the workflow definition: nothing in it decides the package's visibility, which is a registry-side property established when the package is first created. It SHALL therefore be **verified after the first publication**, from a client that is genuinely unauthenticated — a logged-in client pulls a private package happily and would report success either way — and, if the image is not anonymously pullable, made so before the pull command is documented.

Getting this wrong is silent in the only direction that matters: every check passes, the workflow is green, the image exists, and the one audience the documented pull command is written for is the one audience that cannot run it.

An earlier draft of this requirement asserted that a new package is private and must be made public. That was written from received wisdom and is not what this repository observed: the first publication of `4.0.0` was anonymously pullable with no visibility change at all. The requirement is therefore stated as the property to check rather than the mechanism that produces it — the property is what a reader depends on, and the mechanism varies with registry and account settings this specification does not control.

#### Scenario: Anonymous pullability is verified, not assumed

- **WHEN** the first publication creates the package
- **THEN** an unauthenticated pull is attempted against it, and the image is made anonymously pullable if that attempt fails

#### Scenario: An anonymous client can pull

- **WHEN** a client that has not logged in to the registry pulls the published version tag
- **THEN** the pull succeeds

### Requirement: Publication Covers Every Platform The Image Claims

A publication SHALL push a multi-platform image covering `linux/amd64` and `linux/arm64`, such that a client on either platform pulling the version tag receives an image for its own platform.

Partial publication SHALL NOT occur: if one platform cannot be built, nothing is published, rather than a version tag existing for one platform and silently missing for the other. A version that means different things on different machines is worse than a version that is absent.

#### Scenario: Either platform resolves the same tag

- **WHEN** a client on `linux/amd64` and a client on `linux/arm64` each pull the same version tag
- **THEN** each receives an image built for its own platform

#### Scenario: A failed platform blocks the whole publication

- **WHEN** the build for one of the two platforms fails
- **THEN** no tag is published for either

