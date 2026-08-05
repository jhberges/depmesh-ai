#!/usr/bin/env bash
# depmesh-ai installer for Linux and macOS.
#
#   curl -fsSL https://github.com/jhberges/depmesh-ai/releases/latest/download/install.sh | bash
#
# Downloads the release binary, verifies its checksum, installs it, and
# registers the MCP server *globally* (user scope) with every coding CLI and
# IDE it finds — so `vet_dependency` is available in every project, not just
# the one you happen to be in.
#
# It prompts for the install location and for each client it will touch. Piped
# through a pipe it still prompts, by reading /dev/tty; with no terminal at all
# it falls back to the defaults printed below. Flags and environment variables
# override both:
#
#   --dir DIR         install location        ($DEPMESH_INSTALL_DIR)
#   --version TAG     release to install      ($DEPMESH_VERSION, default: latest)
#   --policy FILE     org policy file to pin  ($DEPMESH_POLICY)
#   --clients LIST    claude,copilot,codex,cursor,vscode | all | none
#   --yes             accept every default, never prompt
#   --no-configure    install the binary only
#
# When piping, flags go to bash, not to curl:
#   curl -fsSL .../install.sh | bash -s -- --dir ~/.local/bin --yes
set -euo pipefail

REPO="jhberges/depmesh-ai"
VERSION="${DEPMESH_VERSION:-latest}"
INSTALL_DIR="${DEPMESH_INSTALL_DIR:-}"
POLICY_FILE="${DEPMESH_POLICY:-}"
CLIENTS="${DEPMESH_CLIENTS:-}"
ASSUME_YES=0
CONFIGURE=1

# Private repo, or an internal mirror of the release assets.
TOKEN="${DEPMESH_GITHUB_TOKEN:-${GITHUB_TOKEN:-}}"
DOWNLOAD_BASE="${DEPMESH_DOWNLOAD_BASE:-}"

BIN_NAME="depmesh-ai"
SERVER_NAME="depmesh"

# Telemetry consent. "ask" is the default and degrades to "skip" (leave whatever
# is already configured alone) when there is no terminal, so an unattended run
# can never switch telemetry on.
TELEMETRY="${DEPMESH_TELEMETRY:-ask}"
TELEMETRY_URL="${DEPMESH_TELEMETRY_URL:-}"
TELEMETRY_TOKEN="${DEPMESH_TELEMETRY_TOKEN:-}"
DEFAULT_TELEMETRY_URL="https://depmesh.com/v1/telemetry"
# Must match telemetry.ConfigPath() in the binary: $XDG_CONFIG_HOME, else
# ~/.config — the same on Linux and macOS, so script and binary always agree.
CONSENT_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/depmesh/telemetry.json"

# --- output helpers ---------------------------------------------------------

if [ -t 2 ]; then
	BOLD=$'\033[1m'; BLUE=$'\033[1;34m'; YELLOW=$'\033[1;33m'; RED=$'\033[1;31m'; OFF=$'\033[0m'
else
	BOLD=""; BLUE=""; YELLOW=""; RED=""; OFF=""
fi
say()  { printf '%s==>%s %s\n' "$BLUE" "$OFF" "$*" >&2; }
warn() { printf '%swarning:%s %s\n' "$YELLOW" "$OFF" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$RED" "$OFF" "$*" >&2; exit 1; }

usage() {
	cat >&2 <<-EOF
	depmesh-ai installer for Linux and macOS.

	  curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | bash

	Options (flags go to bash, not curl: ... | bash -s -- --yes):
	  --dir DIR       install location             (\$DEPMESH_INSTALL_DIR)
	  --version TAG   release to install           (\$DEPMESH_VERSION, default: latest)
	  --policy FILE   org policy file to pin       (\$DEPMESH_POLICY)
	  --clients LIST  claude,copilot,codex,cursor,vscode | all | none
	  -y, --yes       accept every default, never prompt
	  --no-configure  install the binary only
	  -h, --help      this message

	Telemetry (off unless you turn it on; you are asked once, interactively):
	  --telemetry            enable without asking
	  --no-telemetry         disable without asking
	  --telemetry-url URL    receiver endpoint      (default: $DEFAULT_TELEMETRY_URL)
	  --telemetry-token KEY  per-tenant ingest key  (\$DEPMESH_TELEMETRY_TOKEN)

	Private repositories: set \$GITHUB_TOKEN (or \$DEPMESH_GITHUB_TOKEN).
	Internal mirror of the assets: set \$DEPMESH_DOWNLOAD_BASE.
	EOF
	exit "${1:-0}"
}

# --- argument parsing -------------------------------------------------------

while [ $# -gt 0 ]; do
	case "$1" in
	--dir)          INSTALL_DIR="${2:?--dir needs a path}"; shift 2 ;;
	--dir=*)        INSTALL_DIR="${1#*=}"; shift ;;
	--version)      VERSION="${2:?--version needs a tag}"; shift 2 ;;
	--version=*)    VERSION="${1#*=}"; shift ;;
	--policy)       POLICY_FILE="${2:?--policy needs a path}"; shift 2 ;;
	--policy=*)     POLICY_FILE="${1#*=}"; shift ;;
	--clients)      CLIENTS="${2:?--clients needs a list}"; shift 2 ;;
	--clients=*)    CLIENTS="${1#*=}"; shift ;;
	-y|--yes)       ASSUME_YES=1; shift ;;
	--no-configure) CONFIGURE=0; shift ;;
	--telemetry)    TELEMETRY=yes; shift ;;
	--no-telemetry) TELEMETRY=no; shift ;;
	--telemetry-url)     TELEMETRY_URL="${2:?--telemetry-url needs a URL}"; TELEMETRY=yes; shift 2 ;;
	--telemetry-url=*)   TELEMETRY_URL="${1#*=}"; TELEMETRY=yes; shift ;;
	--telemetry-token)   TELEMETRY_TOKEN="${2:?--telemetry-token needs a key}"; TELEMETRY=yes; shift 2 ;;
	--telemetry-token=*) TELEMETRY_TOKEN="${1#*=}"; TELEMETRY=yes; shift ;;
	-h|--help)      usage 0 ;;
	*)              printf 'unknown option: %s\n\n' "$1" >&2; usage 2 ;;
	esac
done

# --- prompting (works when the script itself arrived on stdin) --------------

TTY=""
# Test by actually opening /dev/tty, not with -r/-w. In a detached process
# (CI runner, container, systemd unit) the device node exists and passes the
# permission tests, but the open fails because there is no controlling
# terminal — and every prompt would then error and silently take its default.
# The probe runs in a subshell and uses `true`, not `:`. A redirection failure
# on a POSIX *special* builtin (`:` is one) terminates the shell outright under
# dash — silently, and even inside an if-condition — so probing with `:` here
# would abort the installer on exactly the machines it is meant to detect.
if [ "$ASSUME_YES" = 0 ] && (true </dev/tty >/dev/tty) 2>/dev/null; then
	TTY=/dev/tty
fi

# ask PROMPT DEFAULT -> the answer on stdout
ask() {
	local prompt="$1" default="$2" reply=""
	if [ -z "$TTY" ]; then printf '%s' "$default"; return; fi
	printf '%s%s%s [%s]: ' "$BOLD" "$prompt" "$OFF" "$default" > "$TTY"
	IFS= read -r reply < "$TTY" || reply=""
	printf '%s' "${reply:-$default}"
}

# confirm PROMPT DEFAULT(y|n) -> exit status
confirm() {
	local prompt="$1" default="$2" hint="y/N" reply=""
	[ "$default" = y ] && hint="Y/n"
	if [ -z "$TTY" ]; then [ "$default" = y ]; return; fi
	printf '%s%s%s [%s]: ' "$BOLD" "$prompt" "$OFF" "$hint" > "$TTY"
	IFS= read -r reply < "$TTY" || reply=""
	reply="${reply:-$default}"
	case "$reply" in [yY]*) return 0 ;; *) return 1 ;; esac
}

# --- prerequisites ----------------------------------------------------------

say "checking prerequisites"

case "$(uname -s)" in
Linux)  OS=linux ;;
Darwin) OS=darwin ;;
MINGW*|MSYS*|CYGWIN*|Windows_NT)
	die "this is the Unix installer; on Windows run the PowerShell one:
  irm https://github.com/$REPO/releases/latest/download/install.ps1 | iex" ;;
*)      die "unsupported operating system: $(uname -s) (built: linux, darwin, windows)" ;;
esac

case "$(uname -m)" in
x86_64|amd64)  ARCH=amd64 ;;
arm64|aarch64) ARCH=arm64 ;;
*)             die "unsupported CPU architecture: $(uname -m) (built: amd64, arm64)" ;;
esac

DOWNLOAD=""
if command -v curl >/dev/null 2>&1; then
	DOWNLOAD=curl
elif command -v wget >/dev/null 2>&1; then
	DOWNLOAD=wget
else
	die "need curl or wget to download the release"
fi
command -v tar >/dev/null 2>&1 || die "need tar to unpack the release"

SHA_CMD=""
if command -v sha256sum >/dev/null 2>&1; then
	SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SHA_CMD="shasum -a 256"
else
	warn "no sha256sum/shasum found — the download cannot be verified"
fi

# JSON merging needs a parser. Every client we edit by hand has one nearby
# (copilot ships as an npm package, so node is normally present), but if none
# of them exist we print the snippet instead of corrupting a config file.
JSON_TOOL=""
for candidate in python3 node jq; do
	if command -v "$candidate" >/dev/null 2>&1; then JSON_TOOL="$candidate"; break; fi
done

say "platform: $OS/$ARCH"

# fetch URL OUTPUT [ACCEPT] — OUTPUT of "-" writes to stdout
fetch() {
	local url="$1" out="$2" accept="${3:-}"
	if [ "$DOWNLOAD" = curl ]; then
		set -- -fsSL
		[ -n "$TOKEN" ] && set -- "$@" -H "Authorization: Bearer $TOKEN"
		[ -n "$accept" ] && set -- "$@" -H "Accept: $accept"
		[ "$out" = - ] || set -- "$@" -o "$out"
		curl "$@" "$url"
	else
		set -- -q
		[ -n "$TOKEN" ] && set -- "$@" --header="Authorization: Bearer $TOKEN"
		[ -n "$accept" ] && set -- "$@" --header="Accept: $accept"
		[ "$out" = - ] && set -- "$@" -O- || set -- "$@" -O "$out"
		wget "$@" "$url"
	fi
}

# json_get JSON EXPR — read one value out of a JSON document without assuming
# a particular parser is installed.
json_get() {
	[ -n "$1" ] || return 0
	case "$JSON_TOOL" in
	python3) printf '%s' "$1" | python3 -c '
import json, sys
doc = json.load(sys.stdin)
expr = sys.argv[1]
if expr == "tag":
    print(doc.get("tag_name", ""))
else:
    for asset in doc.get("assets", []):
        if asset.get("name") == expr:
            print(asset.get("url", ""))
            break
' "$2" ;;
	node) printf '%s' "$1" | node -e '
let raw = "";
process.stdin.on("data", d => raw += d).on("end", () => {
  const doc = JSON.parse(raw), expr = process.argv[1];
  if (expr === "tag") return console.log(doc.tag_name || "");
  const asset = (doc.assets || []).find(a => a.name === expr);
  console.log(asset ? asset.url : "");
});
' "$2" ;;
	jq)
		if [ "$2" = tag ]; then
			printf '%s' "$1" | jq -r '.tag_name // ""'
		else
			printf '%s' "$1" | jq -r --arg n "$2" '(.assets[]? | select(.name == $n) | .url) // ""' | head -n1
		fi ;;
	*)
		# No parser: tag_name is still greppable, asset ids are not.
		if [ "$2" = tag ]; then
			printf '%s' "$1" |
				sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
		fi ;;
	esac
}

# --- resolve the release version -------------------------------------------

RELEASE_JSON=""
if [ -n "$DOWNLOAD_BASE" ]; then
	[ "$VERSION" = latest ] &&
		die "\$DEPMESH_DOWNLOAD_BASE cannot resolve 'latest' — pass --version vX.Y.Z"
elif [ "$VERSION" = latest ] || [ -n "$TOKEN" ]; then
	if [ "$VERSION" = latest ]; then
		say "resolving latest release"
		api="https://api.github.com/repos/$REPO/releases/latest"
	else
		api="https://api.github.com/repos/$REPO/releases/tags/$VERSION"
	fi
	RELEASE_JSON="$(fetch "$api" - 2>/dev/null || true)"
	if [ "$VERSION" = latest ]; then
		# A malformed or empty response must fall through to the redirect
		# below rather than abort the script.
		VERSION="$(json_get "$RELEASE_JSON" tag 2>/dev/null || true)"
		if [ -z "$VERSION" ] && [ "$DOWNLOAD" = curl ] && [ -z "$TOKEN" ]; then
			# API rate-limited or unreachable: fall back to the /latest redirect.
			final="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
				"https://github.com/$REPO/releases/latest" 2>/dev/null || true)"
			VERSION="${final##*/}"
			[ "$VERSION" = latest ] && VERSION=""
		fi
		[ -n "$VERSION" ] ||
			die "could not determine the latest release; retry with --version vX.Y.Z${TOKEN:+ (is \$GITHUB_TOKEN valid?)}"
	fi
fi
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac

# --- install location -------------------------------------------------------

if [ -z "$INSTALL_DIR" ]; then
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		INSTALL_DIR=/usr/local/bin
	else
		INSTALL_DIR="$HOME/.local/bin"
	fi
	INSTALL_DIR="$(ask "Install $BIN_NAME $VERSION to which directory?" "$INSTALL_DIR")"
fi
# A tilde typed at a prompt arrives as a literal character — the shell never
# saw it, so nothing expanded it. Matching it quoted is the point; expanding it
# here is the fix. shellcheck cannot tell the two situations apart.
# shellcheck disable=SC2088
case "$INSTALL_DIR" in "~"|"~/"*) INSTALL_DIR="$HOME${INSTALL_DIR#\~}" ;; esac

SUDO=""
if [ -d "$INSTALL_DIR" ]; then
	[ -w "$INSTALL_DIR" ] || SUDO=sudo
elif ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
	SUDO=sudo
fi
if [ -n "$SUDO" ]; then
	command -v sudo >/dev/null 2>&1 || die "$INSTALL_DIR is not writable and sudo is unavailable"
	say "$INSTALL_DIR is not writable — using sudo"
	$SUDO mkdir -p "$INSTALL_DIR"
fi

BIN="$INSTALL_DIR/$BIN_NAME"

# --- download, verify, install ---------------------------------------------

TMP="$(mktemp -d "${TMPDIR:-/tmp}/depmesh-install.XXXXXX")"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

ARCHIVE="depmesh_${VERSION}_${OS}_${ARCH}.tar.gz"

# Private repos are only reachable through the API asset endpoint, which serves
# the file itself only when asked for octet-stream — otherwise it returns the
# asset's JSON metadata. Decided here rather than inside asset_url, which runs
# in a command substitution and so cannot export anything.
ACCEPT=""
if [ -z "$DOWNLOAD_BASE" ] && [ -n "$TOKEN" ] && [ -n "$RELEASE_JSON" ]; then
	ACCEPT="application/octet-stream"
fi

# asset_url NAME — where one release asset lives.
asset_url() {
	if [ -n "$DOWNLOAD_BASE" ]; then
		printf '%s/%s' "${DOWNLOAD_BASE%/}" "$1"
	elif [ -n "$ACCEPT" ]; then
		json_get "$RELEASE_JSON" "$1" 2>/dev/null || true
	else
		printf 'https://github.com/%s/releases/download/%s/%s' "$REPO" "$VERSION" "$1"
	fi
}

say "downloading $ARCHIVE"
url="$(asset_url "$ARCHIVE")"
[ -n "$url" ] || die "release $VERSION has no asset named $ARCHIVE (built: linux, darwin × amd64, arm64)"
fetch "$url" "$TMP/$ARCHIVE" "$ACCEPT" ||
	die "download failed: $url
  release $VERSION may not exist for $OS/$ARCH, or the repository is private — set \$GITHUB_TOKEN"

if [ -n "$SHA_CMD" ]; then
	sums="$(asset_url checksums.txt)"
	if [ -n "$sums" ] && fetch "$sums" "$TMP/checksums.txt" "$ACCEPT" 2>/dev/null; then
		# checksums.txt is produced by `sha256sum ./*`, so names carry a ./ prefix.
		want="$(sed -n "s#^\([0-9a-f]\{64\}\)[[:space:]]\{1,\}\.\{0,1\}/\{0,1\}$ARCHIVE\$#\1#p" \
			"$TMP/checksums.txt" | head -n1)"
		got="$($SHA_CMD "$TMP/$ARCHIVE" | cut -d' ' -f1)"
		if [ -z "$want" ]; then
			warn "no checksum published for $ARCHIVE — skipping verification"
		elif [ "$want" != "$got" ]; then
			die "checksum mismatch for $ARCHIVE
  expected $want
  got      $got"
		else
			say "checksum verified"
		fi
	else
		warn "checksums.txt unavailable — skipping verification"
	fi
fi

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" || die "could not unpack $ARCHIVE"
[ -f "$TMP/$BIN_NAME" ] || die "$BIN_NAME missing from $ARCHIVE"

say "installing $BIN"
$SUDO install -m 0755 "$TMP/$BIN_NAME" "$BIN"

installed="$("$BIN" version 2>/dev/null || true)"
[ -n "$installed" ] || die "$BIN did not run after install"
say "installed: $installed"

# PATH sanity: the MCP configs below use the absolute path, so this only
# matters for using depmesh-ai from a shell.
case ":$PATH:" in
*":$INSTALL_DIR:"*)
	other="$(command -v "$BIN_NAME" 2>/dev/null || true)"
	if [ -n "$other" ] && [ "$other" != "$BIN" ]; then
		warn "another $BIN_NAME earlier in PATH shadows this one: $other"
	fi ;;
*)
	warn "$INSTALL_DIR is not on your PATH — add this to your shell profile:
    export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

# --- telemetry consent ------------------------------------------------------

# Asked before client configuration, and deliberately outside the --no-configure
# early exit: whether this machine shares hallucinated package names is a
# separate decision from which editors get the MCP server registered.

# What is configured now, so the prompt can state it and a re-run never flips
# a previous answer silently.
current_url=""
if [ -f "$CONSENT_FILE" ]; then
	current_url="$(sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONSENT_FILE" | head -1)"
fi

# No terminal (piped, CI, --yes) means the question cannot be put to a human,
# so it is not answered on their behalf: leave things exactly as they are.
[ "$TELEMETRY" = ask ] && [ -z "$TTY" ] && TELEMETRY=skip

if [ "$TELEMETRY" = ask ]; then
	cat >&2 <<-EOF

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
	if [ -n "$current_url" ]; then
		printf '\n  Currently: enabled, reporting to %s\n' "$current_url" >&2
		telemetry_default=y
	else
		printf '\n  Currently: disabled\n' >&2
		telemetry_default=n
	fi

	if confirm "  Share hallucinated package names?" "$telemetry_default"; then
		TELEMETRY=yes
		TELEMETRY_URL="$(ask "  Send to" "${TELEMETRY_URL:-${current_url:-$DEFAULT_TELEMETRY_URL}}")"
		[ -n "$TELEMETRY_TOKEN" ] ||
			TELEMETRY_TOKEN="$(ask "  Ingest key, if a hosted tenant issued you one (blank for none)" "")"
	else
		TELEMETRY=no
	fi
	printf '\n' >&2
fi

case "$TELEMETRY" in
yes)
	TELEMETRY_URL="${TELEMETRY_URL:-$DEFAULT_TELEMETRY_URL}"
	# Validated rather than escaped: the consent file is JSON assembled by hand
	# below, so anything that would need an escape is rejected instead.
	#
	# Done with `case`, not `grep`. An empty token is legitimate, and an empty
	# string piped to grep is zero *lines*, which never matches — so a grep
	# check would reject the commonest valid input. `case` also rejects embedded
	# newlines, which an anchored grep pattern would happily let through.
	case "$TELEMETRY_URL" in
	http://*|https://*) ;;
	*) die "telemetry URL must be a plain http(s) URL, got: $TELEMETRY_URL" ;;
	esac
	case "$TELEMETRY_URL" in
	*[!A-Za-z0-9._~:/?#@!\$\&\(\)*+,\;=%-]*)
		die "telemetry URL contains characters that cannot appear in a URL: $TELEMETRY_URL" ;;
	esac
	case "$TELEMETRY_TOKEN" in
	*[!A-Za-z0-9._-]*)
		die "telemetry token may only contain letters, digits, dot, underscore and dash" ;;
	esac

	(umask 077 && mkdir -p "$(dirname "$CONSENT_FILE")")
	if [ -n "$TELEMETRY_TOKEN" ]; then
		body="{\"url\":\"$TELEMETRY_URL\",\"token\":\"$TELEMETRY_TOKEN\"}"
	else
		body="{\"url\":\"$TELEMETRY_URL\"}"
	fi
	(umask 077 && printf '%s\n' "$body" > "$CONSENT_FILE")
	chmod 0600 "$CONSENT_FILE"
	# Say which of the two it is: anonymous reports land in the shared feed and
	# in nobody's dashboard, which is the difference people are surprised by.
	if [ -n "${TELEMETRY_TOKEN:-}" ]; then
		say "telemetry ON — reporting nonexistent-package names to $TELEMETRY_URL, attributed to your tenant"
	else
		say "telemetry ON — reporting nonexistent-package names to $TELEMETRY_URL, anonymously"
		say "    (no ingest key: reports feed the shared slopsquat list, not a tenant dashboard)"
	fi
	printf '    turn it off any time: rm %s\n' "$CONSENT_FILE" >&2
	;;
no)
	if [ -f "$CONSENT_FILE" ]; then
		rm -f "$CONSENT_FILE"
		say "telemetry OFF — removed $CONSENT_FILE"
	else
		say "telemetry OFF — nothing is sent anywhere"
	fi
	;;
skip)
	if [ -n "$current_url" ]; then
		say "telemetry unchanged — still reporting to $current_url"
	else
		say "telemetry OFF (not asked: no terminal). Enable with: --telemetry"
	fi
	;;
esac

[ "$CONFIGURE" = 1 ] || { say "done (skipped client configuration)"; exit 0; }

# --- policy file ------------------------------------------------------------

# Without this, policy is discovered from ./depmesh.policy.json relative to
# wherever the client happened to launch the server — right for per-repo
# policy, wrong for one org-wide file. Pinning it here puts DEPMESH_POLICY in
# every client config we write.
if [ -z "$POLICY_FILE" ] && [ -n "$TTY" ]; then
	if confirm "Pin an organization policy file into the MCP config?" n; then
		POLICY_FILE="$(ask "  Policy file path" "$HOME/.config/depmesh/policy.json")"
	fi
fi
# Literal tilde from a prompt, as above.
# shellcheck disable=SC2088
case "$POLICY_FILE" in "~"|"~/"*) POLICY_FILE="$HOME${POLICY_FILE#\~}" ;; esac
if [ -n "$POLICY_FILE" ]; then
	case "$POLICY_FILE" in
	/*) ;;
	*)  POLICY_FILE="$PWD/$POLICY_FILE" ;;
	esac
	[ -f "$POLICY_FILE" ] || warn "policy file does not exist yet: $POLICY_FILE
         create it before using the server, or clients will fail to start"
fi

# --- JSON config writing ----------------------------------------------------

json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

# server_entry FLAVOR -> the JSON object for one MCP server
server_entry() {
	local env_json=""
	if [ -n "$POLICY_FILE" ]; then
		env_json=",\"env\":{\"DEPMESH_POLICY\":\"$(json_escape "$POLICY_FILE")\"}"
	fi
	case "$1" in
	copilot)
		# Copilot calls stdio servers "local", and omitting `tools` leaves the
		# tool unlisted in the session.
		printf '{"type":"local","command":"%s","args":["serve"],"tools":["*"]%s}' \
			"$(json_escape "$BIN")" "$env_json" ;;
	*)
		printf '{"command":"%s","args":["serve"]%s}' "$(json_escape "$BIN")" "$env_json" ;;
	esac
}

# json_merge FILE ROOTKEY NAME ENTRY — create or update FILE.ROOTKEY.NAME
json_merge() {
	local file="$1" root="$2" name="$3" entry="$4" dir
	dir="$(dirname "$file")"
	mkdir -p "$dir"
	if [ -s "$file" ]; then
		cp "$file" "$file.depmesh.bak"
	fi
	case "$JSON_TOOL" in
	python3)
		python3 - "$file" "$root" "$name" "$entry" <<-'PY'
			import json, os, sys
			path, root, name, entry = sys.argv[1:5]
			data = {}
			if os.path.exists(path) and os.path.getsize(path):
			    with open(path) as fh:
			        data = json.load(fh)
			if not isinstance(data, dict):
			    raise SystemExit("%s is not a JSON object" % path)
			data.setdefault(root, {})[name] = json.loads(entry)
			tmp = path + ".depmesh.tmp"
			with open(tmp, "w") as fh:
			    json.dump(data, fh, indent=2)
			    fh.write("\n")
			os.replace(tmp, path)
		PY
		;;
	node)
		node -e '
			const fs = require("fs");
			const [file, root, name, entry] = process.argv.slice(1);
			let data = {};
			try {
				const text = fs.readFileSync(file, "utf8").trim();
				if (text) data = JSON.parse(text);
			} catch (e) { if (e.code !== "ENOENT") throw e; }
			data[root] = data[root] || {};
			data[root][name] = JSON.parse(entry);
			fs.writeFileSync(file + ".depmesh.tmp", JSON.stringify(data, null, 2) + "\n");
			fs.renameSync(file + ".depmesh.tmp", file);
		' "$file" "$root" "$name" "$entry"
		;;
	jq)
		[ -s "$file" ] || printf '{}\n' > "$file"
		jq --arg r "$root" --arg n "$name" --argjson e "$entry" \
			'.[$r] = (.[$r] // {}) | .[$r][$n] = $e' "$file" > "$file.depmesh.tmp"
		mv "$file.depmesh.tmp" "$file"
		;;
	*)
		warn "no python3, node or jq available — add this to $file by hand:
    \"$root\": { \"$name\": $entry }"
		return 1
		;;
	esac
}

# --- client detection -------------------------------------------------------

has_client() {
	case "$1" in
	claude)  command -v claude  >/dev/null 2>&1 ;;
	copilot) command -v copilot >/dev/null 2>&1 || [ -d "$HOME/.copilot" ] ;;
	codex)   command -v codex   >/dev/null 2>&1 ;;
	cursor)  [ -d "$HOME/.cursor" ] ;;
	vscode)  command -v code    >/dev/null 2>&1 ;;
	esac
}

client_label() {
	case "$1" in
	claude)  echo "Claude Code (user scope)" ;;
	copilot) echo "GitHub Copilot CLI (~/.copilot)" ;;
	codex)   echo "Codex CLI" ;;
	cursor)  echo "Cursor (IDE)" ;;
	vscode)  echo "VS Code (IDE)" ;;
	esac
}

configure_claude() {
	# `mcp add` refuses to overwrite, so drop any previous registration first.
	claude mcp remove "$SERVER_NAME" --scope user >/dev/null 2>&1 || true
	if [ -n "$POLICY_FILE" ]; then
		claude mcp add "$SERVER_NAME" --scope user \
			-e "DEPMESH_POLICY=$POLICY_FILE" -- "$BIN" serve >/dev/null
	else
		claude mcp add "$SERVER_NAME" --scope user -- "$BIN" serve >/dev/null
	fi
}

configure_copilot() {
	json_merge "$HOME/.copilot/mcp-config.json" mcpServers "$SERVER_NAME" "$(server_entry copilot)"
}

configure_codex() {
	if ! codex mcp --help >/dev/null 2>&1; then
		local toml="[mcp_servers.$SERVER_NAME]
command = \"$BIN\"
args = [\"serve\"]"
		[ -n "$POLICY_FILE" ] && toml="$toml
env = { DEPMESH_POLICY = \"$POLICY_FILE\" }"
		warn "this codex build has no 'mcp' subcommand — add to ~/.codex/config.toml:
$toml"
		return 1
	fi
	codex mcp remove "$SERVER_NAME" >/dev/null 2>&1 || true
	if [ -n "$POLICY_FILE" ] && codex mcp add --help 2>&1 | grep -q -- '--env'; then
		codex mcp add "$SERVER_NAME" --env "DEPMESH_POLICY=$POLICY_FILE" -- "$BIN" serve >/dev/null
	else
		codex mcp add "$SERVER_NAME" -- "$BIN" serve >/dev/null
		if [ -n "$POLICY_FILE" ]; then
			warn "codex mcp add takes no --env here; set DEPMESH_POLICY in ~/.codex/config.toml"
		fi
	fi
}

configure_cursor() {
	json_merge "$HOME/.cursor/mcp.json" mcpServers "$SERVER_NAME" "$(server_entry generic)"
}

configure_vscode() {
	if ! code --help 2>&1 | grep -q -- '--add-mcp'; then
		warn "this VS Code build has no --add-mcp; add depmesh via the MCP: Add Server command"
		return 1
	fi
	local entry
	entry="$(server_entry generic)"
	code --add-mcp "{\"name\":\"$SERVER_NAME\",${entry#\{}" >/dev/null 2>&1
}

# --- configure ---------------------------------------------------------------

DETECTED=""
for client in claude copilot codex cursor vscode; do
	has_client "$client" && DETECTED="$DETECTED $client"
done

if [ -z "$DETECTED" ]; then
	say "no supported CLI or IDE found — nothing to configure"
	cat >&2 <<-EOF

	Register the MCP server manually with any client:
	  command: $BIN
	  args:    ["serve"]
	EOF
	exit 0
fi

SELECTED=""
case "$CLIENTS" in
none)
	SELECTED="" ;;
""|all)
	if [ -n "$TTY" ] && [ "$CLIENTS" != all ]; then
		say "found:$DETECTED"
		for client in $DETECTED; do
			confirm "Register depmesh with $(client_label "$client")?" y &&
				SELECTED="$SELECTED $client"
		done
	else
		SELECTED="$DETECTED"
	fi ;;
*)
	for client in $(printf '%s' "$CLIENTS" | tr ',' ' '); do
		# Constant subject on purpose: this is the POSIX idiom for "is
		# $client one of these", with the spaces making it whole-word.
		# shellcheck disable=SC2194
		case " claude copilot codex cursor vscode " in
		*" $client "*)
			has_client "$client" && SELECTED="$SELECTED $client" ||
				warn "$client requested but not found on this machine" ;;
		*) warn "unknown client: $client" ;;
		esac
	done ;;
esac

FAILED=""
for client in $SELECTED; do
	say "configuring $(client_label "$client")"
	if ! "configure_$client"; then
		FAILED="$FAILED $client"
	fi
done

# --- summary ----------------------------------------------------------------

printf '\n%sdepmesh-ai %s installed.%s\n' "$BOLD" "$VERSION" "$OFF" >&2
printf '  binary:  %s\n' "$BIN" >&2
[ -n "$POLICY_FILE" ] && printf '  policy:  %s\n' "$POLICY_FILE" >&2
if [ -f "$CONSENT_FILE" ]; then
	printf '  telemetry: on  (%s)\n' "$CONSENT_FILE" >&2
else
	printf '  telemetry: off\n' >&2
fi
if [ -n "$SELECTED" ]; then
	printf '  clients: %s\n' "${SELECTED# }" >&2
fi
[ -n "$FAILED" ] && printf '  needs manual setup:%s\n' "$FAILED" >&2
cat >&2 <<-EOF

	Try it:
	  $BIN_NAME vet npm express
	  $BIN_NAME vet npm this-package-definitely-does-not-exist-zz9

	Restart your CLIs/IDEs to pick up the new MCP server, then ask the agent
	to vet a dependency before installing it.
EOF
