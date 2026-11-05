#!/bin/bash -e

BASEDIR="$(realpath "$(dirname "$0")/..")"

if [[ -z "$1" ]]; then
    VERSION=$(<"$BASEDIR/VERSION")
else
    VERSION=$1
fi

# RELEASE: VERSION = 0.1.0-BUILD => VERSION = 0.1.0
if [[ ${VERSION} == *-BUILD* ]]; then
    version_digits=$(echo "$VERSION" | cut -d'-' -f1)
    VERSION="${version_digits}"
fi

echo "$VERSION" > "$BASEDIR/VERSION"
