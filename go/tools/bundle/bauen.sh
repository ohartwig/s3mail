#!/bin/sh
# Baut das macOS-App-Bundle. Ein Bundle ist nur eine Verzeichnisstruktur mit
# einer Info.plist - das laesst sich von Linux aus erzeugen, ohne Apple-Werkzeug
# und ohne den Mac, den die Portierung gerade losgeworden ist.
#
#   bauen.sh <binary> <zielverzeichnis> <version>
set -eu
BINARY="$1"; ZIEL="$2"; VERSION="$3"
APP="$ZIEL/s3mail.app"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BINARY" "$APP/Contents/MacOS/s3mail"
chmod +x "$APP/Contents/MacOS/s3mail"
sed "s/__VERSION__/$VERSION/g" "$(dirname "$0")/Info.plist" > "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
echo "Bundle gebaut: $APP"
