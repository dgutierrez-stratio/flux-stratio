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
# Copy next to the target, then rename over it: cp onto a binary that is
# running (a flux stratio command waiting at a prompt, say) fails with
# "Text file busy", while a rename swaps the file and leaves the running
# process on the old one.
cp "$TMPDIR/flux-stratio" "$PLUGIN_DIR/.flux-stratio.new"
mv -f "$PLUGIN_DIR/.flux-stratio.new" "$PLUGIN_DIR/flux-stratio"

echo "Installed to $PLUGIN_DIR/flux-stratio"
