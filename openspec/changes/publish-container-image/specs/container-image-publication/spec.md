## ADDED Requirements

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

The version tag SHALL be treated as immutable: once a version has been published, a later publication SHALL NOT overwrite it with different contents.

The distinction between the two tags is what makes either useful. The version tag is the one a reader can be told to pull in order to run a known artifact; if it can be rewritten, it names nothing. `latest` exists for a reader who has no version in hand, and is documented as a convenience rather than as a reproducible reference.

#### Scenario: Publishing the newest version moves `latest`

- **WHEN** version `X.Y.Z` is published and no higher version has been published before
- **THEN** both `X.Y.Z` and `latest` resolve to that image

#### Scenario: Republishing an older version leaves `latest` alone

- **WHEN** version `X.Y.Z` is published while a higher version already has a published image
- **THEN** `X.Y.Z` resolves to the newly published image and `latest` still resolves to the higher version's image

#### Scenario: A published version is not overwritten

- **WHEN** a publication is attempted for a version that already has a published image
- **THEN** it fails without pushing, unless the operator has explicitly opted into replacing it

#### Scenario: No moving major or minor pointer exists

- **WHEN** the published tags are listed after a release
- **THEN** they are the version and `latest`, and no major-only or major-minor tag is present

### Requirement: An Existing Tag Can Be Published Deliberately

The publish workflow SHALL provide a manually triggered path that publishes a git tag which already exists, building that tag's tree rather than the current state of `main`.

Without it, the first image would appear only when the next version-bumping commit lands, so documentation naming a pull command would be false from the moment it is written until some unrelated change ships. This is a bootstrap path and a recovery path — a release whose publication failed for a transient reason is republished the same way — and it is the reason the immutability check above exists, since a manual trigger taking a tag name is where a released version would otherwise be overwritten by a typo.

#### Scenario: An already-released version is published after the fact

- **WHEN** the manual path is triggered for an existing git tag that has no published image
- **THEN** an image is published from that tag's tree, tagged with that version

#### Scenario: The manual path builds the tag, not the branch

- **WHEN** the manual path publishes tag `vX.Y.Z` while `main` has advanced beyond it
- **THEN** the image contents are those of `vX.Y.Z`, not of `main`

#### Scenario: The manual path refuses a version that already has an image

- **WHEN** the manual path is triggered for a version whose image is already published, without an explicit opt-in to replace it
- **THEN** it fails without pushing

### Requirement: The Published Image Is Pullable Without Credentials

The published image SHALL be obtainable by a client that has not authenticated to the registry, so long as the repository itself is public.

A registry package inherits its repository's visibility when it is first created, which means this property is established by the first publication and is not observable from the workflow definition. It SHALL therefore be verified against the registry after the first publication rather than assumed.

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
