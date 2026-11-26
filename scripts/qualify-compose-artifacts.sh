#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Prepare verified, read-only release inputs for a disposable hosted job.
set -euo pipefail

work=$1
product=$2
: "${GITHUB_REPOSITORY:?}" "${GITHUB_OUTPUT:?}" "${STAGED_RUN:?}" "${STAGED_SHA:?}" "${BASELINE_TAG:?}"
[[ "$STAGED_RUN" =~ ^[0-9]+$ && "$STAGED_SHA" =~ ^[0-9a-f]{40}$ ]]
[[ "$BASELINE_TAG" =~ ^(carbonpanel-|mod-)?v[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$GITHUB_ACTIONS" == true && "$RUNNER_ENVIRONMENT" == github-hosted ]]
[[ "$(git -C "$product" rev-parse HEAD)" == "$STAGED_SHA" ]]
mkdir -p "$work/evidence"
evidence="$work/evidence/provenance.txt"
printf 'checkout=%s\nproduct=%s\nstaging_run=%s\n' "$(git rev-parse HEAD)" "$STAGED_SHA" "$STAGED_RUN" | tee "$evidence"

repo="/repos/$GITHUB_REPOSITORY"
gh api "$repo/actions/runs/$STAGED_RUN" > "$work/run.json"
workflow_head=$(jq -er --arg repo "$GITHUB_REPOSITORY" '
    select(.path == ".github/workflows/release.yml" and .conclusion == "success" and
      .head_repository.full_name == $repo) |
    .head_sha | select(test("^[0-9a-f]{40}$"))' "$work/run.json")
printf 'release_workflow_head=%s\n' "$workflow_head" | tee -a "$evidence"
gh api "$repo/actions/runs/$STAGED_RUN/artifacts?per_page=100" > "$work/artifacts.json"

artifact_count() {
  jq -r --arg name "$1" '[.artifacts[] | select(.name == $name and .expired == false)] | length' \
    "$work/artifacts.json"
}

# An ambiguous primary artifact is a hard failure, never a reason to take the
# fallback path: two unexpired candidates cannot prove which bytes were staged.
primary_count=$(artifact_count release-image-context)
if (( primary_count > 1 )); then
  echo "ambiguous unexpired release-image-context artifacts: $primary_count" >&2
  exit 1
fi
stage_count=$(artifact_count qualified-stable-stage)
if (( stage_count > 1 )); then
  echo "ambiguous unexpired qualified-stable-stage artifacts: $stage_count" >&2
  exit 1
fi

fetch_artifact() {
  local name=$1 record id digest zip
  record=$(jq -ec --arg name "$name" '[.artifacts[] | select(.name == $name and .expired == false)] |
    if length == 1 then .[0] else error("artifact missing or ambiguous") end' "$work/artifacts.json")
  id=$(jq -er '.id' <<< "$record")
  digest=$(jq -er '.digest | select(test("^sha256:[0-9a-f]{64}$"))' <<< "$record")
  zip="$work/$name.zip"
  gh api "$repo/actions/artifacts/$id/zip" > "$zip"
  printf '%s  %s\n' "${digest#sha256:}" "$zip" | sha256sum -c
  printf 'actions_artifact=%s id=%s digest=%s\n' "$name" "$id" "$digest" | tee -a "$evidence"
  unzip -q "$zip" -d "$work/$name"
}

fetch_artifact release-bundle-linux-amd64
mapfile -t bundles < <(find "$work/release-bundle-linux-amd64" -name 'carbon-panel*_linux_amd64.tar.gz' -type f)
[[ "${#bundles[@]}" == 1 ]]
bundle="${bundles[0]}"
mkdir "$work/bundle"
tar -xzf "$bundle" -C "$work/bundle"

candidate_version=$(jq -er --arg sha "$STAGED_SHA" '
  select(.commit == $sha and .os == "linux" and .arch == "amd64") |
  .version | sub("^v";"") | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?$"))' "$work/bundle/BUILD-INFO.json")

# Current build-only stages retain an OCI archive with an identity receipt.
# Earlier hosted qualification runs can replay a raw context when no receipt
# was generated. Record the distinction in the evidence.
context="$work/release-image-context"
stage_digest=
if (( stage_count == 1 )); then
  fetch_artifact qualified-stable-stage
  stage="$work/qualified-stable-stage/stage.json"
  stage_digest=$(jq -er '.digest | select(test("^sha256:[0-9a-f]{64}$"))' "$stage")
  RELEASE_VERSION="carbonpanel-v$candidate_version" RELEASE_COMMIT="$STAGED_SHA" \
    EXPECTED_DIGEST="$stage_digest" python3 -m scripts.stable_recovery verify-stage \
    --source-run "$work/run.json" --journal "$stage"
  fetch_artifact verified-release-oci
  oci_tar="$work/verified-release-oci/image.oci.tar"
  test -s "$oci_tar"
  test "$(cat "$work/verified-release-oci/distribution-digest.txt")" = "$stage_digest"
  command -v skopeo >/dev/null || { sudo apt-get update && sudo apt-get install -y skopeo; }
  skopeo inspect --raw "oci-archive:$oci_tar" > "$work/staged-index.json"
  test "sha256:$(sha256sum "$work/staged-index.json" | cut -d' ' -f1)" = "$stage_digest"
  printf 'stage_commit=%s\nstage_digest=%s\n' "$STAGED_SHA" "$stage_digest" | tee -a "$evidence"
elif (( primary_count == 1 )); then
  fetch_artifact release-image-context
  for program in carbon-panel carbon-storage; do
    cmp "$work/bundle/$program" "$context/linux_amd64/$program"
  done
else
  echo "stage has no qualified OCI receipt or legacy release-image-context" >&2
  exit 1
fi

printf 'candidate_version=%s\n' "$candidate_version" | tee -a "$evidence"
sha256sum "$bundle" "$work/bundle/carbon-panel" "$work/bundle/carbon-storage" \
  "$product/docker/Dockerfile.release" "$product/docker-compose.yml" \
  "$product/scripts/compose-upgrade.sh" "$product/scripts/compose-upgrade-rollback.sh" | tee -a "$evidence"

gh release download "$BASELINE_TAG" -R "$GITHUB_REPOSITORY" -p release-manifest.json -D "$work"
baseline_digest=$(jq -er '.image_digest | select(test("^sha256:[0-9a-f]{64}$"))' "$work/release-manifest.json")
baseline_commit=$(jq -er '.commit | select(test("^[0-9a-f]{40}$"))' "$work/release-manifest.json")
baseline_repo=$(jq -er '.github_image | select(test("^ghcr\\.io/[a-z0-9._/-]+:[0-9A-Za-z.-]+$")) | split(":")[0]' "$work/release-manifest.json")
baseline="$baseline_repo@$baseline_digest"
printf 'baseline_tag=%s\nbaseline_commit=%s\nbaseline_image=%s\n' \
  "$BASELINE_TAG" "$baseline_commit" "$baseline" | tee -a "$evidence"
sha256sum "$work/release-manifest.json" | tee -a "$evidence"

# A loopback-only registry makes the shipped --pull always path usable without
# publishing an image outside the disposable runner.
docker run -d --name compose-qualification-registry -p 127.0.0.1:5000:5000 registry:2
for ((i=0; i<30; i++)); do
  if curl --silent --fail http://127.0.0.1:5000/v2/ >/dev/null; then break; fi
  sleep 1
done
curl --silent --show-error --fail http://127.0.0.1:5000/v2/ >/dev/null
tag="127.0.0.1:5000/carbonpanel:candidate"
if [[ -n "$stage_digest" ]]; then
  skopeo copy --all --preserve-digests --dest-tls-verify=false \
    --digestfile "$work/copied-digest" "oci-archive:$oci_tar" "docker://$tag"
  test "$(cat "$work/copied-digest")" = "$stage_digest"
  candidate="${tag%:*}@$stage_digest"
  skopeo inspect --raw --tls-verify=false "docker://$candidate" > "$work/copied-index.json"
  cmp "$work/staged-index.json" "$work/copied-index.json"
  provenance=staged-oci-archive
  exact=true
else
  docker buildx build --platform linux/amd64 --load --progress plain \
    -f "$product/docker/Dockerfile.release" -t "$tag" \
    --label "org.opencontainers.image.version=$candidate_version" \
    --label "org.opencontainers.image.revision=$STAGED_SHA" \
    --build-arg "APP_VERSION=$candidate_version" "$context" 2>&1 | tee "$work/evidence/image-build.txt"
  docker push "$tag"
  candidate=$(docker image inspect "$tag" --format '{{json .RepoDigests}}' |
    jq -er '.[] | select(test("^127\\.0\\.0\\.1:5000/carbonpanel@sha256:[0-9a-f]{64}$"))')
  provenance=local-build-from-release-image-context
  exact=false
fi
printf 'candidate_image=%s\ncandidate_image_provenance=%s\ncandidate_image_matches_staged_oci=%s\n' \
  "$candidate" "$provenance" "$exact" | tee -a "$evidence"
printf 'baseline_image=%s\nbaseline_commit=%s\ncandidate_image=%s\ncandidate_version=%s\n' \
  "$baseline" "$baseline_commit" "$candidate" "$candidate_version" >> "$GITHUB_OUTPUT"
