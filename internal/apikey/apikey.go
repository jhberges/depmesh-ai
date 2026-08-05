// Package apikey handles the shared secret between a served gate and the
// developer machines delegating to it.
//
// The gate is a write path to disk: every answered request appends an audit
// record, and a failed audit write withholds the decision. Left open, that
// turns "anyone who can reach the port" into "anyone who can fill the disk
// and take the gate down with it". A key in front means an unauthenticated
// flood is refused before it reaches the registry, the audit log, or the
// policy engine.
package apikey

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// Prefix marks generated keys as depmesh credentials, so they are
// recognizable in a config file and to secret scanners.
const Prefix = "dmk_"

// EnvVar configures the serving side; UpstreamEnvVar the delegating side.
// Both exist because a key on argv is visible to every process on the host
// via ps, and the MCP server is frequently launched by an IDE that passes
// arguments but no environment.
const (
	EnvVar         = "DEPMESH_API_KEY"
	UpstreamEnvVar = "DEPMESH_UPSTREAM_KEY"
)

// Off is the explicit opt-out, spelled the same way on the flag and in the
// environment. Nothing else disables authentication: a missing value means
// "generate one", never "serve without".
const Off = "off"

// Generate returns a new random key. 32 bytes of crypto/rand — far past
// guessing, and short enough to paste into a client config.
func Generate() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("could not generate an API key: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Matches compares a presented key against the configured one in constant
// time, so a caller cannot recover the key one byte at a time by measuring
// how long the comparison takes.
func Matches(configured, presented string) bool {
	if configured == "" {
		return true // authentication disabled
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(presented)) == 1
}

// FromHeader pulls the key out of an Authorization header, accepting the
// bearer form this project sends and a bare key for the benefit of hand-run
// curl commands.
func FromHeader(header string) string {
	trimmed := strings.TrimSpace(header)
	const scheme = "bearer" // case-insensitive per RFC 7235
	if len(trimmed) >= len(scheme) && strings.EqualFold(trimmed[:len(scheme)], scheme) {
		rest := trimmed[len(scheme):]
		// The scheme word must stand alone: "Bearer k" and a bare "Bearer"
		// are the auth form, "bearerish" is somebody's actual key.
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return strings.TrimSpace(rest)
		}
	}
	return trimmed
}
