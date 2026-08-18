# depmesh-ai as a container, for the deployment the README calls "self-hosted,
# inside your network": one gate holding the org's single policy file and single
# audit log, so developer machines and CI runners need no registry egress.
#
#   docker run -p 8385:8385 \
#     -v ./depmesh.policy.json:/etc/depmesh/policy.json:ro \
#     -v depmesh-audit:/var/log/depmesh \
#     ghcr.io/jhberges/depmesh-ai

# The go directive in go.mod is the floor; this tag must not fall below it.
# BUILDPLATFORM keeps the toolchain native and lets Go cross-compile to
# TARGETARCH — buildx would otherwise emulate the whole build under QEMU for
# the arm64 image, which costs minutes to produce an identical static binary.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
# Stamped into the binary and reported by `depmesh-ai version`. Left as the
# in-tree default for local builds; release.yml passes the real tag.
ARG VERSION=dev

WORKDIR /src

# No `go mod download` layer, and that is not an oversight: the module has zero
# external dependencies and no go.sum, which CI enforces on every commit. There
# is nothing to fetch, so the build needs no network at all.
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/

# Identical flags to .github/workflows/release.yml, deliberately: the container
# and the tarball should be the same binary, not two builds that merely agree
# most of the time.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X github.com/jhberges/depmesh-ai/internal/gate.Version=${VERSION}" \
      -o /out/depmesh-ai ./cmd/depmesh-ai

# The runtime stage has no shell, so the audit directory has to be built here
# and copied across with its ownership already correct.
RUN mkdir -p /out/varlog

# Distroless static rather than scratch, for two concrete reasons rather than
# taste:
#
#   * /etc/ssl/certs/ca-certificates.crt — the gate calls npm, PyPI, Maven and
#     deps.dev over HTTPS. On scratch every vet fails x509 verification.
#   * /etc/passwd with nonroot:65532 — audit.actor() calls os/user.Current(),
#     which under CGO_ENABLED=0 is a pure-Go parse of /etc/passwd. On scratch
#     that errors, falls through to an unset $USER, and every audit record from
#     a caller that sent no X-Depmesh-Actor header gets an empty actor. A
#     compliance log that cannot name who asked is not doing its job.
#
# Not Alpine: a package manager and a libc inside a supply-chain tool's image is
# the thing this project's zero-dependency stance exists to argue against.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a

ARG VERSION=dev

LABEL org.opencontainers.image.title="depmesh-ai" \
      org.opencontainers.image.description="Vet open-source dependencies before adopting them — HTTP gate mode." \
      org.opencontainers.image.source="https://github.com/jhberges/depmesh-ai" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=builder /out/depmesh-ai /usr/local/bin/depmesh-ai
COPY --from=builder --chown=65532:65532 /out/varlog /var/log/depmesh

# Policy is mounted, not baked. Pointing $DEPMESH_POLICY at the mount makes the
# path *explicit* to policy.Load, which means a missing file is a startup error
# rather than a silent fall-through to "no policy" — a compliance gate that
# quietly serves with nothing to enforce is the worse failure. Run with
# `-e DEPMESH_POLICY=` to evaluate the image without one.
ENV DEPMESH_POLICY=/etc/depmesh/policy.json
ENV DEPMESH_LISTEN=:8385

USER 65532:65532
EXPOSE 8385
VOLUME ["/var/log/depmesh"]

# No HEALTHCHECK: distroless has no shell and no curl to run one with. GET
# /healthz is what a Kubernetes probe or an external checker should call —
# see the README for both.

ENTRYPOINT ["/usr/local/bin/depmesh-ai"]

# --audit-log here overrides the policy file's audit_log field, which is how
# the image guarantees decisions are recorded even when a mounted policy forgot
# to say where. To let the policy decide instead, override the command without
# the flag: `docker run ... ghcr.io/jhberges/depmesh-ai api`.
CMD ["api", "--audit-log", "/var/log/depmesh/decisions.jsonl"]
