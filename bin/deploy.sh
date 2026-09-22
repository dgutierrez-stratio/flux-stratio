#!/bin/bash -e

BASEDIR="$(realpath "$(dirname "$0")/..")"
DIR="$BASEDIR/bin"
EXTENSION="tar.gz"
GROUP_ID="repository.paas.flux-stratio"
GROUP_ID_NEXUS=${GROUP_ID//.//}

if [[ -z "$1" ]]; then
	VERSION=$(<"$BASEDIR/VERSION")
else
	VERSION=$1
fi

FILE="$DIR/flux-stratio-${VERSION}.${EXTENSION}"

if [ -r "$FILE" ]; then
	echo "Uploading flux-stratio-$VERSION..."
	curl -sS -f -u stratio:${NEXUSPASS} --upload-file "$FILE" "http://qa.int.stratio.com/${GROUP_ID_NEXUS}/"
else
	echo "Run 'make package' first"
	exit 1
fi
