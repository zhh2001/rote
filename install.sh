#!/bin/sh
# rote installer: download the latest release for this OS/arch and install it.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/zhh2001/rote/main/install.sh | sh
#
# Environment overrides:
#   ROTE_VERSION   install a specific tag (e.g. v1.2.3) instead of the latest
#   INSTALL_DIR    install location (default: /usr/local/bin)

set -eu

REPO="zhh2001/rote"
BINARY="rote"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

info() { printf '%s\n' "rote-install: $*"; }
err() {
	printf '%s\n' "rote-install: error: $*" >&2
	exit 1
}

# download <url> <dest> saves the URL to a file, following redirects.
download() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		err "need curl or wget to download files"
	fi
}

# Preserve the hash tool's exit status: a pipeline to awk would hide failures.
# sha256 <file> prints the file's SHA-256, or returns non-zero on failure.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		checksum="$(sha256sum "$1")" || return 1
	elif command -v shasum >/dev/null 2>&1; then
		checksum="$(shasum -a 256 "$1")" || return 1
	else
		return 1
	fi
	printf '%s\n' "${checksum%% *}"
}

# Detect the OS. uname -s ("Linux"/"Darwin") already matches the goreleaser
# archive naming (title-cased OS), so it is used verbatim.
os="$(uname -s)"
case "$os" in
Linux | Darwin) ;;
*) err "unsupported OS: $os (only Linux and Darwin are supported)" ;;
esac

# Map the machine architecture to the goreleaser archive naming.
machine="$(uname -m)"
case "$machine" in
x86_64 | amd64) arch="x86_64" ;;
aarch64 | arm64) arch="arm64" ;;
*) err "unsupported architecture: $machine" ;;
esac

if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
	err "need sha256sum or shasum to verify downloads; refusing to install without verification"
fi

# Build the release download base URL. Archive names carry no version, so the
# tag is not needed up front: without ROTE_VERSION we use GitHub's
# /releases/latest/download/ redirect, which resolves to the newest release's
# assets without calling (and being rate-limited by) the GitHub API.
if [ -n "${ROTE_VERSION:-}" ]; then
	base_url="https://github.com/${REPO}/releases/download/${ROTE_VERSION}"
	info "installing ${BINARY} ${ROTE_VERSION} for ${os}/${arch}"
else
	base_url="https://github.com/${REPO}/releases/latest/download"
	info "installing the latest ${BINARY} for ${os}/${arch}"
fi

# The asset name MUST match archives.name_template in .goreleaser.yaml:
#   {{ .ProjectName }}_{{ title .Os }}_{{ amd64 -> x86_64 | else .Arch }}
asset="${BINARY}_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

info "downloading ${asset}..."
download "${base_url}/${asset}" "${tmp}/${asset}" ||
	err "failed to download ${asset} from ${base_url}"

# Verification is mandatory and must finish before extraction or installation.
info "downloading checksums.txt..."
download "${base_url}/checksums.txt" "${tmp}/checksums.txt" ||
	err "failed to download checksums.txt from ${base_url}; refusing to install without verification"
expected="$(awk -v f="$asset" '
	{ sub(/\r$/, "") }
	$2 == f {
		matches++
		if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/) invalid = 1
		hash = tolower($1)
	}
	END {
		if (matches != 1 || invalid) exit 1
		print hash
	}
' "${tmp}/checksums.txt")" ||
	err "checksums.txt must contain exactly one valid SHA-256 for ${asset}"
actual="$(sha256 "${tmp}/${asset}")" || err "failed to calculate SHA-256 for ${asset}"
[ "$actual" = "$expected" ] || err "checksum mismatch for ${asset}"
info "checksum verified"

info "extracting..."
tar -xzf "${tmp}/${asset}" -C "$tmp"
[ -f "${tmp}/${BINARY}" ] || err "archive did not contain ${BINARY}"
chmod +x "${tmp}/${BINARY}"

# Install, elevating with sudo if the target directory is not writable.
info "installing to ${INSTALL_DIR}..."
if [ -w "$INSTALL_DIR" ]; then
	mv "${tmp}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
elif command -v sudo >/dev/null 2>&1; then
	info "${INSTALL_DIR} is not writable; elevating with sudo"
	sudo mkdir -p "$INSTALL_DIR"
	sudo mv "${tmp}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
	err "${INSTALL_DIR} is not writable and sudo is unavailable; set INSTALL_DIR to a writable path"
fi

info "installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"
"${INSTALL_DIR}/${BINARY}" version
info "run '${BINARY} init' to create a starter config, then '${BINARY}'"
