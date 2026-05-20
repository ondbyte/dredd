#!/usr/bin/env bash
# Build the custom dredd-test images required by the all-languages Docker
# test for languages without a usable public image.
#
# Usage:
#   ./dreddtest/images/build.sh           # build every dredd-test/* image
#   ./dreddtest/images/build.sh <name>    # build a single image (e.g. cobol)
#
# Images produced:
#   dredd-test/assembly-nasm:latest
#   dredd-test/cobol:latest
#   dredd-test/freebasic:latest
#   dredd-test/kotlin-1.3:latest
#   dredd-test/kotlin-2.1:latest
#   dredd-test/objective-c:latest
#   dredd-test/pascal-fpc:latest
set -euo pipefail

cd "$(dirname "$0")"

images=(assembly-nasm cobol freebasic kotlin-1.3 kotlin-2.1 objective-c pascal-fpc)

if [[ $# -gt 0 ]]; then
    images=("$@")
fi

for name in "${images[@]}"; do
    dockerfile="${name}.Dockerfile"
    if [[ ! -f "$dockerfile" ]]; then
        echo "no Dockerfile for ${name} (expected ${dockerfile})" >&2
        exit 1
    fi
    tag="dredd-test/${name}:latest"
    echo "==> building ${tag} from ${dockerfile}"
    docker build -f "${dockerfile}" -t "${tag}" .
done

echo "done."
