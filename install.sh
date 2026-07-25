#!/usr/bin/env bash
# Install the depmesh-ai CLI from a GitHub release, and ask — once — whether
# this machine should help improve the slopsquat sensor.
#
#   curl -fsSL https://raw.githubusercontent.com/jhberges/depmesh-ai/main/install.sh | bash
#   ./install.sh --bin-dir /usr/local/bin --version v0.3.0
#
# Unattended (CI, config management) — nothing is ever asked, and telemetry
# stays off unless it is switched on explicitly:
#
#   ./install.sh --no-telemetry
#   ./install.sh --telemetry --telemetry-token dmk_...
#   ./install.sh --telemetry --telemetry-url https://depmesh.internal/v1/telemetry
set -euo pipefail

REPO="jhberges/depmesh-ai"
DEFAULT_TELEMETRY_URL="https://depmesh.com/v1/telemetry"

VERSION="${DEPMESH_VERSION:-}"
BIN_DIR="${DEPMESH_BIN_DIR:-$HOME/.local/bin}"
# yes | no | ask. Default is ask; a non-interactive run turns that into "no".
TELEMETRY="${DEPMESH_TELEMETRY:-ask}"
TELEMETRY_URL="${DEPMESH_TELEMETRY_URL:-}"
TELEMETRY_TOKEN="${DEPMESH_TELEMETRY_TOKEN:-}"

config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
CONSENT_FILE="$config_home/depmesh/telemetry.json"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<EOF
usage: install.sh [options]

  --version VERSION        release to install (default: latest)
  --bin-dir DIR            install location (default: \$HOME/.local/bin)
  --telemetry              enable telemetry without asking
  --no-telemetry           disable telemetry without asking
  --telemetry-url URL      receiver endpoint (default: $DEFAULT_TELEMETRY_URL)
  --telemetry-token TOKEN  per-tenant ingest key for the receiver
  -h, --help               this text

Environment equivalents: DEPMESH_VERSION, DEPMESH_BIN_DIR, DEPMESH_TELEMETRY
(yes|no), DEPMESH_TELEMETRY_URL, DEPMESH_TELEMETRY_TOKEN.
EOF
	exit 0
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--version) VERSION="${2:?--version needs a value}"; shift 2;;
		--bin-dir) BIN_DIR="${2:?--bin-dir needs a value}"; shift 2;;
		--telemetry) TELEMETRY=yes; shift;;
		--no-telemetry) TELEMETRY=no; shift;;
		--telemetry-url) TELEMETRY_URL="${2:?--telemetry-url needs a value}"; TELEMETRY=yes; shift 2;;
		--telemetry-token) TELEMETRY_TOKEN="${2:?--telemetry-token needs a value}"; TELEMETRY=yes; shift 2;;
		-h|--help) usage;;
		*) die "unknown option: $1 (try --help)";;
	esac
done

case "$TELEMETRY" in yes|no|ask) ;; *) die "DEPMESH_TELEMETRY must be yes, no or ask";; esac

# --- tools -------------------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1"; }
	fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO- "$1"; }
	fetch_to() { wget -qO "$2" "$1"; }
else
	die "curl or wget is required"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is required to verify the download"
fi

# --- platform ----------------------------------------------------------------

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
	x86_64|amd64) arch=amd64;;
	arm64|aarch64) arch=arm64;;
	*) die "unsupported architecture: $arch";;
esac
case "$os" in
	linux|darwin) ;;
	*) die "unsupported OS: $os — see https://github.com/$REPO/releases for Windows builds";;
esac

# --- version -----------------------------------------------------------------

if [[ -z "$VERSION" ]]; then
	say "resolving latest release"
	VERSION="$(fetch "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
	[[ -n "$VERSION" ]] || die "could not determine the latest release; pass --version vX.Y.Z"
fi
[[ "$VERSION" == v* ]] || VERSION="v$VERSION"

archive="depmesh_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

# --- download and verify -----------------------------------------------------

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

say "downloading $archive ($VERSION)"
fetch_to "$base/$archive" "$work/$archive" || die "download failed: $base/$archive"
fetch_to "$base/checksums.txt" "$work/checksums.txt" || die "could not fetch checksums.txt"

expected="$(awk -v name="$archive" '{ sub(/^\.\//, "", $2); if ($2 == name) print $1 }' "$work/checksums.txt")"
[[ -n "$expected" ]] || die "$archive is not listed in checksums.txt"
actual="$(sha256 "$work/$archive")"
[[ "$expected" == "$actual" ]] || die "checksum mismatch for $archive (expected $expected, got $actual)"
say "checksum OK"

tar -xzf "$work/$archive" -C "$work"
[[ -f "$work/depmesh-ai" ]] || die "the archive does not contain a depmesh-ai binary"

# --- install -----------------------------------------------------------------

mkdir -p "$BIN_DIR" || die "cannot create $BIN_DIR"
install -m 0755 "$work/depmesh-ai" "$BIN_DIR/depmesh-ai" ||
	die "cannot write to $BIN_DIR — pass --bin-dir, or re-run with sudo"
installed="$("$BIN_DIR/depmesh-ai" version 2>/dev/null || echo "depmesh-ai $VERSION")"
say "installed $installed to $BIN_DIR/depmesh-ai"

# --- telemetry consent -------------------------------------------------------

# Current state, so the prompt can tell the user what they already chose and
# re-running the installer does not silently flip it.
current_url=""
if [[ -f "$CONSENT_FILE" ]]; then
	current_url="$(sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONSENT_FILE" | head -1)"
fi

# A piped install (curl | bash) has the script on stdin, so ask the terminal
# directly. No terminal means no question: leave telemetry as it is.
tty_in=""
[[ -e /dev/tty ]] && { : </dev/tty; } 2>/dev/null && tty_in=/dev/tty

if [[ "$TELEMETRY" == ask && -z "$tty_in" ]]; then
	TELEMETRY=skip
fi

if [[ "$TELEMETRY" == ask ]]; then
	cat <<EOF

  Help improve depmesh-ai?

  When a package is REJECTed because it does not exist on its registry, that
  name is almost always one an AI assistant hallucinated. Sharing those names
  builds a live feed of slopsquat targets — so they can be tracked before
  attackers register them.

    sent      ecosystem, package name, timestamp, tool version
    not sent  username, hostname, IP-derived data, repository or project
              names — and nothing whatsoever about packages that do exist

  This is off unless you say yes, and you can change your mind at any time
  (the answer is a single file: $CONSENT_FILE).
EOF
	if [[ -n "$current_url" ]]; then
		printf '\n  Currently: enabled, reporting to %s\n' "$current_url"
		default=Y; prompt="[Y/n]"
	else
		printf '\n  Currently: disabled\n'
		default=N; prompt="[y/N]"
	fi
	printf '\n  Share hallucinated package names? %s ' "$prompt"
	read -r answer <"$tty_in" || answer=""
	[[ -n "$answer" ]] || answer="$default"
	case "$answer" in
		[Yy]*) TELEMETRY=yes;;
		*) TELEMETRY=no;;
	esac

	if [[ "$TELEMETRY" == yes ]]; then
		suggested="${TELEMETRY_URL:-${current_url:-$DEFAULT_TELEMETRY_URL}}"
		printf '  Send to [%s]: ' "$suggested"
		read -r reply <"$tty_in" || reply=""
		TELEMETRY_URL="${reply:-$suggested}"
		if [[ -z "$TELEMETRY_TOKEN" ]]; then
			printf '  Ingest key, if a hosted tenant issued you one (blank for none): '
			read -r TELEMETRY_TOKEN <"$tty_in" || TELEMETRY_TOKEN=""
		fi
	fi
	printf '\n'
fi

case "$TELEMETRY" in
	yes)
		TELEMETRY_URL="${TELEMETRY_URL:-$DEFAULT_TELEMETRY_URL}"
		# Validated rather than escaped: the consent file is JSON assembled by
		# hand here, so anything needing an escape is rejected instead.
		url_pattern='^https?://[^[:space:]"\\]+$'
		token_pattern='^[A-Za-z0-9._-]*$'
		[[ "$TELEMETRY_URL" =~ $url_pattern ]] ||
			die "telemetry URL must be a plain http(s) URL, got: $TELEMETRY_URL"
		[[ "$TELEMETRY_TOKEN" =~ $token_pattern ]] ||
			die "telemetry token may only contain letters, digits, dot, underscore and dash"

		( umask 077 && mkdir -p "$(dirname "$CONSENT_FILE")" )
		if [[ -n "$TELEMETRY_TOKEN" ]]; then
			body="{\"url\":\"$TELEMETRY_URL\",\"token\":\"$TELEMETRY_TOKEN\"}"
		else
			body="{\"url\":\"$TELEMETRY_URL\"}"
		fi
		( umask 077 && printf '%s\n' "$body" > "$CONSENT_FILE" )
		chmod 0600 "$CONSENT_FILE"
		say "telemetry ON — reporting nonexistent-package names to $TELEMETRY_URL"
		echo "    turn it off any time: rm $CONSENT_FILE"
		;;
	no)
		if [[ -f "$CONSENT_FILE" ]]; then
			rm -f "$CONSENT_FILE"
			say "telemetry OFF — removed $CONSENT_FILE"
		else
			say "telemetry OFF — nothing is sent anywhere"
		fi
		;;
	skip)
		if [[ -n "$current_url" ]]; then
			say "telemetry unchanged — still reporting to $current_url"
		else
			say "telemetry OFF (not asked: no terminal). Enable with: $0 --telemetry"
		fi
		;;
esac

# --- PATH hint ---------------------------------------------------------------

case ":$PATH:" in
	*":$BIN_DIR:"*) ;;
	*) printf '\n\033[1;33mnote:\033[0m %s is not on your PATH — add it:\n  export PATH="%s:$PATH"\n' \
		"$BIN_DIR" "$BIN_DIR";;
esac

cat <<EOF

Try it:
  depmesh-ai vet npm express
  depmesh-ai vet npm this-package-definitely-does-not-exist-zz9
EOF
