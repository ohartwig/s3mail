#!/bin/sh
# Builds the macOS app bundle. A bundle is just a directory tree with an
# Info.plist - that can be produced from Linux, without Apple tooling and without
# the Mac the port has just got rid of.
#
#   build.sh <binary> <destination> <version>
set -eu
BINARY="$1"; DEST="$2"; VERSION="$3"
APP="$DEST/s3mail.app"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BINARY" "$APP/Contents/MacOS/s3mail"
chmod +x "$APP/Contents/MacOS/s3mail"
sed "s/__VERSION__/$VERSION/g" "$(dirname "$0")/Info.plist" > "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
echo "bundle built: $APP"
