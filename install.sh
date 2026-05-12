#!/usr/bin/env sh
# Install ralph from the latest GitHub Release.
#
# Verifies the SHA256 from the published checksums.txt and, on macOS, strips
# the com.apple.quarantine xattr so Gatekeeper does not block the unsigned
# binary on first run.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/chuntley/go-ralph-go/main/install.sh | sh
#
# Env overrides:
#   RALPH_INSTALL_DIR   Install destination (default: ~/.local/bin)
#   RALPH_VERSION       Release tag to install (default: latest)

# Wrapped in main() so that a truncated `curl | sh` (network drop mid-stream)
# leaves the function undefined and exits cleanly, instead of executing a
# half-read script. The trailing `main "$@"` only runs once the full script
# has been received.
main() {
    set -eu

    REPO="chuntley/go-ralph-go"
    INSTALL_DIR="${RALPH_INSTALL_DIR:-$HOME/.local/bin}"
    VERSION="${RALPH_VERSION:-latest}"

    err() { printf 'install: %s\n' "$*" >&2; exit 1; }
    info() { printf 'install: %s\n' "$*"; }

    need() {
        command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"
    }

    need curl
    need tar
    need uname

    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        darwin|linux) ;;
        *) err "unsupported OS: $os (use 'go install' instead)" ;;
    esac

    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) err "unsupported arch: $arch" ;;
    esac

    # Pre-flight: confirm INSTALL_DIR is (or can be) writable before downloading.
    # Without this, mv at the end dies with a bare "Permission denied" after a
    # full download/checksum cycle — common when users override to /usr/local/bin
    # without sudo.
    check_dir=$INSTALL_DIR
    while [ ! -d "$check_dir" ] && [ "$check_dir" != "/" ] && [ -n "$check_dir" ]; do
        check_dir=$(dirname "$check_dir")
    done
    if [ ! -w "$check_dir" ]; then
        err "install dir not writable: $INSTALL_DIR (try RALPH_INSTALL_DIR=\$HOME/.local/bin, or re-run with sudo for system paths)"
    fi

    # Resolve the release tag without depending on jq.
    if [ "$VERSION" = "latest" ]; then
        # GitHub redirects /releases/latest to /releases/tag/<TAG>.
        VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
            "https://github.com/${REPO}/releases/latest" \
            | sed 's#.*/tag/##')
        [ -n "$VERSION" ] || err "could not resolve latest release tag"
    fi

    # Sanity-check the tag. If redirect parsing returned a full URL (captive
    # portal, proxy interstitial, GitHub redirect format change), the next step
    # would build a garbage download URL and surface a confusing 404. A tag
    # shouldn't contain slashes or whitespace.
    case "$VERSION" in
        */*|*' '*|*"$(printf '\t')"*)
            err "resolved release tag looks malformed: '$VERSION' (set RALPH_VERSION=<tag> to pin explicitly)" ;;
    esac

    ver_no_v=${VERSION#v}
    tarball="ralph_${ver_no_v}_${os}_${arch}.tar.gz"
    base="https://github.com/${REPO}/releases/download/${VERSION}"

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    info "downloading $tarball ($VERSION)"
    curl -fsSL "${base}/${tarball}" -o "${tmp}/${tarball}"
    curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"

    info "verifying SHA256"
    expected=$(awk -v f="$tarball" '$2 == f {print $1}' "${tmp}/checksums.txt")
    [ -n "$expected" ] || err "no checksum entry for $tarball in checksums.txt"

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "${tmp}/${tarball}" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "${tmp}/${tarball}" | awk '{print $1}')
    else
        err "need sha256sum or shasum to verify download"
    fi

    [ "$expected" = "$actual" ] || err "checksum mismatch: expected $expected, got $actual"

    info "extracting"
    tar -xzf "${tmp}/${tarball}" -C "$tmp" ralph

    mkdir -p "$INSTALL_DIR"
    mv "${tmp}/ralph" "${INSTALL_DIR}/ralph"
    chmod +x "${INSTALL_DIR}/ralph"

    # Strip quarantine so Gatekeeper does not block the (un-notarized) binary.
    # Safe to run after checksum verification above.
    if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
        xattr -d com.apple.quarantine "${INSTALL_DIR}/ralph" 2>/dev/null || true
    fi

    info "installed ${INSTALL_DIR}/ralph"

    case ":$PATH:" in
        *":${INSTALL_DIR}:"*) ;;
        *) info "note: ${INSTALL_DIR} is not on \$PATH — add it to your shell rc" ;;
    esac
}

main "$@"
