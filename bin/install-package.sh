#!/bin/bash -e

BASEDIR="$(realpath "$(dirname "$0")/..")"
DIR="$BASEDIR/bin"
EXTENSION="tar.gz"
PLUGIN_DIR="$HOME/.fluxcd/plugins"

if [[ -z "$1" ]]; then
	VERSION=$(<"$BASEDIR/VERSION")
else
	VERSION=$1
fi

FILE="$DIR/flux-stratio-${VERSION}.${EXTENSION}"

if [ ! -r "$FILE" ]; then
	echo "Run 'make package' first"
	exit 1
fi

echo "Installing flux-stratio-$VERSION from $FILE..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

tar xzf "$FILE" -C "$TMPDIR"
mkdir -p "$PLUGIN_DIR"
chmod +x "$TMPDIR/flux-stratio"
cp "$TMPDIR/flux-stratio" "$PLUGIN_DIR/flux-stratio"

echo "Installed to $PLUGIN_DIR/flux-stratio"
