# Telemetry protocol

The opt-in slopsquat sensor sends one kind of message to one endpoint. This
document is the contract between the two sides:

| Side | Repository | Code |
|---|---|---|
| Producer | **depmesh-ai** (this repo) | `internal/telemetry/telemetry.go` |
| Consumer | [depmesh-cloud](https://github.com/jhberges/depmesh-cloud) | `internal/cloud/server` (`handleIngest`), `internal/cloud/store` (`Event`) |

They live in separate repositories, so this is a published interface rather than
an internal detail. **Changes must be additive** — see [Compatibility](#compatibility).

Implementing your own receiver is the point: it is one `POST` handler. Nothing
about `depmesh-ai` assumes the hosted service.

## When anything is sent

Only when **all** of the following hold:

1. A receiver is configured. Telemetry is off by default and there is no
   implicit fallback to any hosted endpoint — see [Resolution order](#resolution-order).
2. The verdict is `REJECT`.
3. The reason is that the package **does not exist** on its registry
   (`Facts.Exists` is present and false).

A registry outage never triggers a report: "unreachable" and "does not exist"
are distinct states throughout the tool. Real packages, low-scoring packages,
and policy rejections are never reported.

The rationale: a package name that an assistant proposed and that does not exist
is, with high probability, a hallucination — and therefore a slopsquatting target
before an attacker registers it.

## Request

```http
POST {endpoint} HTTP/1.1
Content-Type: application/json
Authorization: Bearer {ingest key}      # omitted when no key is configured
```

```json
{
  "kind": "nonexistent_package",
  "ecosystem": "npm",
  "package": "some-hallucinated-name",
  "time": "2026-08-02T09:41:00Z",
  "tool_version": "v0.3.0"
}
```

| Field | Type | Notes |
|---|---|---|
| `kind` | string | Currently always `nonexistent_package`. Treat as an open set. |
| `ecosystem` | string | `npm`, `pypi`, `maven`, … |
| `package` | string | The name as requested. |
| `time` | RFC 3339 | Client clock. **The reference server overwrites this** with its own receive time; do not rely on it being preserved. |
| `tool_version` | string | Producer build version. Receivers must tolerate any string. |

Deliberately **not** sent: usernames, hostnames, repository or project names,
file paths, IP-derived data, or any other context. The payload above is the
whole payload.

The client is fire-and-forget with a 3-second timeout. Any failure — network,
DNS, 5xx, timeout — is discarded silently and never affects the vet result or
the exit code.

## Response

| Status | Body | Meaning |
|---|---|---|
| `202` | `{"status":"accepted"}` | Stored. |
| `400` | `{"error":"invalid JSON: …"}` | Unparseable body. |
| `400` | `{"error":"kind, ecosystem and package are required"}` | Missing required field. |
| `401` | `{"error":"missing or unknown ingest key"}` | Bearer token absent or unrecognised. |
| `500` | `{"error":"storage failure"}` | Receiver-side persistence error. |

The producer ignores the status code. It is specified so that alternative
receivers behave predictably for anyone debugging with `curl`.

## Server-side rules

A conforming receiver **must**:

- Derive the tenant from the ingest key, **never** from the payload. The wire
  format has no tenant field for exactly this reason.
- Bound the request body. The reference server caps it at **16 KiB** and rejects
  anything larger.
- Treat `time` as advisory and stamp its own receive time.

## Resolution order

The endpoint is resolved most-specific-first (`telemetry.Resolve`):

1. `telemetry_url` in the policy file — a repo or org decision, reviewed and
   version-controlled with the rest of the policy.
2. `$DEPMESH_TELEMETRY_URL` — per-shell or per-CI override.
3. The consent file the installer writes after the user agrees —
   `$XDG_CONFIG_HOME/depmesh/telemetry.json`, else `~/.config/depmesh/telemetry.json`.
   This path is used on both Linux and macOS so the shell installer and the
   binary always agree.

An empty result means disabled, which is what every unconfigured installation
gets. None of these three sources exists unless somebody created it.

The ingest key comes from `$DEPMESH_TELEMETRY_TOKEN`, or from the consent file.
**A stored key is only ever sent to the endpoint stored beside it**: if policy or
the environment redirects telemetry to a different host, the key stays behind.
Leaking a tenant's key to an unrelated host is precisely the accident a telemetry
feature must not have.

## Compatibility

The two repositories release independently, so neither side may assume the other
has been updated.

- **Add optional fields only.** Every new field must be ignorable.
- **Never repurpose an existing field**, and never change the meaning of a value.
- `kind` is an open set. Receivers must not reject unknown kinds.
- Producers must tolerate any response, including bodies they do not understand.

The fixture at [`testdata/telemetry_event.json`](../internal/telemetry/testdata/telemetry_event.json)
is committed identically to both repositories: `depmesh-ai` asserts it marshals
to that shape, `depmesh-cloud` asserts it unmarshals from it. If you change the
wire format, both fixtures change together — that is the drift alarm.
