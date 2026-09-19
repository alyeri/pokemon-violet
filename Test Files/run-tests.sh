#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(dirname -- "$script_dir")
temp_root=$(mktemp -d "${TMPDIR:-/tmp}/pokemon-violet-tests.XXXXXX")
trap 'rm -rf -- "$temp_root"' EXIT HUP INT TERM

cp "$repo_root"/*.go "$repo_root/go.mod" "$repo_root/go.sum" "$temp_root/"
cp -R "$repo_root/proto" "$temp_root/proto"
cp "$script_dir"/*_test.go "$temp_root/"

cd "$temp_root"
go test ./... -count=1
go vet ./...
