#!/bin/sh
# gonka-proxy — download the latest rolling build from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/bugakov/gonka-proxy/main/install.sh | sh
#
# CI publishes a `latest` GitHub release (rolling, overwritten on every main
# push) holding the linux/amd64 proxy binary. This script fetches it and drops
# it into the target directory, backing up any existing binary first, and can
# optionally restart the oxmgr-managed service the same way the README's
# manual deploy does — only here the binary comes from a release, not a CI
# artifact download.
#
# Options:
#   --dir DIR       install into DIR instead of auto-detecting (~/.local/bin)
#   --restart       restart the oxmgr service after replacing the binary
#   --service NAME  oxmgr service name (default: gonka-proxy)
#   --asset NAME    override the release asset name (default: gonka-proxy-linux-amd64)
#   --version       print script version and exit
#   --dry-run       resolve target + URL, do not install
#
# POSIX sh. No external deps beyond curl/wget.

set -u

REPO="bugakov/gonka-proxy"
RELEASES_URL="https://github.com/${REPO}/releases/latest/download"

SERVICE="gonka-proxy"
TRIES=3
VERSION="1.0.0"
DRY_RUN=0
DO_RESTART=0

log() { printf '%s\n' "[gonka-install] $*"; }
die() { printf '%s\n' "[gonka-install] ERROR: $*" >&2; exit 1; }

# ---- args ----
while [ "$#" -gt 0 ]; do
    case "$1" in
        --dir) INSTALL_DIR="${2:-}"; shift 2 ;;
        --restart) DO_RESTART=1; shift ;;
        --service) SERVICE="${2:-}"; shift 2 ;;
        --asset) ASSET_OVERRIDE="${2:-}"; shift 2 ;;
        --version) echo "gonka-install $VERSION"; exit 0 ;;
        --dry-run) DRY_RUN=1; shift ;;
        *) die "unknown argument: $1" ;;
    esac
done

# ---- platform detection -> asset name ----
OS="$(uname -s 2>/dev/null || echo unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"

case "$OS" in
    Linux) os="linux" ;;
    Darwin) os="darwin" ;;
    *) die "unsupported OS: $OS (CI builds a linux/amd64 binary)" ;;
esac

case "$ARCH" in
    x86_64|amd64) arch="amd64" ;;
    *) die "unsupported architecture: $ARCH (CI builds linux/amd64)" ;;
esac

ASSET="${ASSET_OVERRIDE:-gonka-proxy-${os}-${arch}}"
URL="${RELEASES_URL}/${ASSET}"

# ---- resolve install dir ----
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

if [ "$DRY_RUN" = "1" ]; then
    printf 'OS=%s ARCH=%s\nASSET=%s\nURL=%s\nINSTALL_DIR=%s\nRESTART=%s\nSERVICE=%s\n' \
        "$OS" "$ARCH" "$ASSET" "$URL" "$INSTALL_DIR" "$DO_RESTART" "$SERVICE"
    exit 0
fi

mkdir -p "$INSTALL_DIR" 2>/dev/null || die "cannot create $INSTALL_DIR"
if [ ! -w "$INSTALL_DIR" ]; then
    die "$INSTALL_DIR is not writable (use --dir or run as a user with write access)"
fi

# ---- download helper ----
fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fL -S \
            --connect-timeout 10 \
            --max-time 120 \
            --retry 1 \
            --retry-delay 2 \
            --progress-bar \
            "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -O- --tries="$TRIES" --timeout=30 --waitretry=3 --progress=dot:meter "$1"
    else
        die "need curl or wget"
    fi
}

# fetch_asset $URL -> file, with retries, sanity size check and atomic move.
# Usage: fetch_asset "$URL" "$TMPDIR" "$NAME" "$TARGET"
fetch_asset() {
    URL="$1"; TMPDIR="$2"; NAME="$3"; TARGET="$4"
    TMP="${TMPDIR}/${NAME}.$$"
    n=0; ok=0
    while [ "$n" -lt "$TRIES" ]; do
        n=$((n + 1))
        rm -f "$TMP"
        if fetch "$URL" > "$TMP"; then ok=1; break; fi
        log "download attempt $n/$TRIES failed; retrying..."
        [ "$n" -lt "$TRIES" ] && sleep 3
    done
    [ "$ok" = "1" ] || { rm -f "$TMP"; die "download failed after $TRIES attempts ($NAME)"; }

    SZ=$(wc -c < "$TMP" 2>/dev/null || echo 0)
    if [ "$SZ" -lt 100000 ]; then
        head -c 300 "$TMP" >&2; echo >&2
        rm -f "$TMP"
        die "downloaded file looks wrong (${SZ} bytes); release asset '$NAME' may be stale"
    fi
    chmod +x "$TMP" 2>/dev/null || die "cannot chmod $NAME"

    if [ -e "$TARGET" ] || [ -L "$TARGET" ]; then
        BAK="$TARGET.bak.$(date +%Y%m%d%H%M%S)"
        mv "$TARGET" "$BAK" 2>/dev/null && log "backed up old binary to $BAK"
    fi
    mv "$TMP" "$TARGET" 2>/dev/null || die "cannot move $NAME into $INSTALL_DIR"
    log "installed $NAME -> $TARGET"
}

TMPDIR="${TMPDIR:-/tmp}"
BAK_DIR_TMP="${TMPDIR}/gonka-install.$$"
mkdir -p "$BAK_DIR_TMP" || die "cannot create $BAK_DIR_TMP"

TARGET="$INSTALL_DIR/gonka-proxy"
log "Downloading $ASSET from $URL"
fetch_asset "$URL" "$BAK_DIR_TMP" "$ASSET" "$TARGET"

rm -rf "$BAK_DIR_TMP"

# ---- restart (optional) ----
if [ "$DO_RESTART" = "1" ]; then
    if command -v oxmgr >/dev/null 2>&1; then
        log "restarting $SERVICE via oxmgr"
        oxmgr restart "$SERVICE" || die "oxmgr restart $SERVICE failed"
    else
        log "oxmgr not found; binary replaced, please restart manually"
    fi
else
    log "binary replaced. Restart to apply, e.g.:"
    log "  oxmgr restart $SERVICE"
fi

log "Done."

exit 0
