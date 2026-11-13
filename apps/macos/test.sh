#!/bin/bash
# Uses only the Testing framework supplied with Swift/Xcode, no dependencies.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
DEVELOPER="$(xcode-select -p)"
CLT_FRAMEWORKS="$DEVELOPER/Library/Developer/Frameworks"
if [[ -d "$CLT_FRAMEWORKS/Testing.framework" ]]; then
    # Some standalone Command Line Tools releases omit these search paths.
    swift test --package-path "$HERE" --disable-xctest \
        -Xswiftc "-F$CLT_FRAMEWORKS" \
        -Xlinker -rpath -Xlinker "$CLT_FRAMEWORKS" \
        -Xlinker -rpath -Xlinker "$DEVELOPER/Library/Developer/usr/lib"
else
    swift test --package-path "$HERE" --disable-xctest
fi
