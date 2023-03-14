#!/usr/bin/env bash
die() { echo "${1:-nope}" >&2; exit "${2:-1}"; }

# dep check
m=()
for d in git gh go; do
    hash $d && continue
    m+=("$d")
done 2>/dev/null
(( ${#m[@]} > 0 )) && die "missing deps: ${m[*]}"

# what're we building
tool=$(basename "$(git rev-parse --show-toplevel)")

# make the build dir
mkdir -p ./build

# whats the ver
# default version string is the branch name, short hash, and "edge"
branch=$(git rev-parse --abbrev-ref HEAD)
hash=$(git rev-parse --short HEAD)
ver="$branch-$hash-edge"
release=nope
# if the CHANGELOG is in the current commit, we're releasing
git diff --quiet HEAD^ HEAD -- CHANGELOG || release=yep

# release being `yep` only matters on main, we'll use the top line of CHANGELOG as the ver
[[ $release == "yep" ]] && [[ $branch == "main" ]] && ver=$(head -n1 CHANGELOG)

# lets go
ld="-X main.version=$ver"
for p in linux darwin darwin-arm windows; do
    arch=amd64
    [[ $p == darwin-arm ]] && { p=darwin; arch=arm64; }
    fn="$tool-$p-$arch-$ver"
    [[ $p == windows ]] && fn+=".exe"

    echo ":golang::$p: $fn"
    GOARCH=$arch GOOS=$p CGO_ENABLED=0 \
        go build -ldflags="$ld" -o "./build/$fn" || die "cant build"
done

[[ $release == "nope" ]] && die "built successfully, not releasing!" 0

# don't publish releases from branches
[[ $branch == "main" ]] || die "oops, we only release on main" 1

gh release create "$ver" build/*
