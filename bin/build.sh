#!/usr/bin/env bash
die() { echo "${1:-nope}" >&2; exit "${2:-1}"; }

# dep check
# m=()
# for d in go hub; do
#     hash $d && continue
#     m+=("$d")
# done 2>/dev/null
# (( ${#m[@]} > 0 )) && die "missing deps: ${m[*]}"

# make the build dir
mkdir -p ./build

# whats the ver
# default version string is "edge"
ver=edge
release=nope
# if the CHANGELOG is in the current commit, we're releasing using the top line as the version
git diff --quiet HEAD^ HEAD -- CHANGELOG || { release=yep; ver=$(head -n1 CHANGELOG);}

# lets go
ld="-X main.version=$ver"
for p in linux darwin darwin-arm windows; do
    arch=amd64
    [[ $p == darwin-arm ]] && { p=darwin; arch=arm64; }
    fn=lash-$p-$arch-"$ver"
    [[ $p == windows ]] && fn+=".exe"

    echo ":golang::$p: $fn"
    GOARCH=amd64 GOOS=$p CGO_ENABLED=0 \
        go build -ldflags="$ld" -o ./build/$fn || die "cant build"
done

[[ $release == "nope" ]] && die "not releasing!" 0

gh release create "$ver" build/*
