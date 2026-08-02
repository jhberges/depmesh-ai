#!/usr/bin/env bash
# Assert that install.sh and install.ps1 offer the same options.
#
# The two installers drifted badly once already: the Unix one grew a telemetry
# consent prompt that the Windows one never got, so Windows users were never
# asked a question the project describes as always being asked. That is a
# privacy-posture bug, not a cosmetic one, and it went unnoticed because
# nothing compared the two files.
#
# Run from the repository root:  scripts/check-installer-parity.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sh_file="$here/install.sh"
ps_file="$here/install.ps1"

# Options that intentionally differ in spelling between the two platforms.
# Everything else must match after kebab-case -> PascalCase conversion.
alias_of() {
	case "$1" in
	dir) printf 'InstallDir' ;;   # --dir is the Unix spelling of -InstallDir
	*)   printf '' ;;
	esac
}

# --foo-bar -> FooBar
pascal() {
	printf '%s' "$1" | awk -F- '{ for (i = 1; i <= NF; i++)
		printf "%s%s", toupper(substr($i, 1, 1)), substr($i, 2) }'
}

# Long options from install.sh's argument-parsing case block. Both `--foo)` and
# `--foo=*)` forms appear; take the bare name once. `-y|--yes` is matched too.
# Note the leading-whitespace alternative: the case arms are tab-indented, so a
# pattern anchored only to ^ or | silently finds almost nothing.
sh_opts="$(
	sed -n '/^while \[ \$# -gt 0 \]/,/^done$/p' "$sh_file" |
		grep -oE '(^[[:space:]]*|\|)--[a-z][a-z-]*' |
		sed 's/.*--//' | sort -u |
		grep -vx 'help'
)"

# Parameters from install.ps1's param() block.
ps_opts="$(
	sed -n '/^param(/,/^)$/p' "$ps_file" |
		grep -oE '\$[A-Za-z][A-Za-z0-9]*' |
		tr -d '$' | sort -u
)"

status=0

# Every Unix option must exist on Windows.
for opt in $sh_opts; do
	want="$(alias_of "$opt")"
	[ -n "$want" ] || want="$(pascal "$opt")"
	if ! printf '%s\n' "$ps_opts" | grep -qx "$want"; then
		printf 'install.sh has --%s but install.ps1 has no -%s\n' "$opt" "$want" >&2
		status=1
	fi
done

# ...and the reverse. Build the expected PowerShell name for each Unix option
# once, then look for anything on the Windows side that is unaccounted for.
expected=""
for opt in $sh_opts; do
	want="$(alias_of "$opt")"
	[ -n "$want" ] || want="$(pascal "$opt")"
	expected="$expected$want
"
done
for opt in $ps_opts; do
	# Help is excluded on both sides: PowerShell supplies it via CmdletBinding,
	# the Unix script via -h/--help, and neither needs the other to declare it.
	[ "$opt" = "Help" ] && continue
	if ! printf '%s' "$expected" | grep -qx "$opt"; then
		printf 'install.ps1 has -%s but install.sh has no matching long option\n' "$opt" >&2
		status=1
	fi
done

if [ "$status" -eq 0 ]; then
	printf 'installer parity OK (%s options)\n' "$(printf '%s\n' "$sh_opts" | wc -l | tr -d ' ')"
fi
exit "$status"
