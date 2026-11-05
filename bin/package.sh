#!/bin/bash -e

BASEDIR="$(realpath "$(dirname "$0")/..")"
DIR="$BASEDIR/bin"
EXTENSION="tar.gz"

if [[ -z "$1" ]]; then
	VERSION=$(<"$BASEDIR/VERSION")
else
	VERSION=$1
fi

if [ -r "$DIR/flux-stratio" ]; then
	echo "Packaging flux-stratio-$VERSION..."
	tar czf "$DIR/flux-stratio-${VERSION}.${EXTENSION}" -C "$DIR" flux-stratio
else
	echo "Run 'make build' first"
	exit 1
fi
