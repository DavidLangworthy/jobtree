# Builds both jobtree binaries for the kind e2e harness (Track F — TESTINFRA-2,
# docs/project/make-it-real-plan.md) and for real deployment via
# deploy/helm/gpu-fleet (values.yaml's controller.image / scheduler.image):
#
#   docker build --target manager   -t jobtree-controller .   (also the default)
#   docker build --target scheduler -t jobtree-scheduler  .
#
# ONE builder stage, two images. This used to be two Dockerfiles, each doing its
# own `FROM golang`, its own `go mod download`, and its own compile of the whole
# dependency graph — for two binaries out of the same module. In CI that step was
# 210s of a 307s e2e job. Sharing the builder means the second image's build is a
# cache hit on every layer up to its final COPY.
#
# The cache mounts keep the module and build caches across invocations on the
# same daemon, so an incremental rebuild recompiles only what changed. They need
# BuildKit, which is the default in Docker >= 23 and in `docker buildx`.
#
# Multi-stage: build with the full Go toolchain, ship static binaries on a
# distroless base with no shell — matches the "no wildcard RBAC / minimal attack
# surface" discipline the rest of the chart already holds to (R22).
FROM golang:1.26.5 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Both binaries in one invocation: they share a dependency graph, so the second
# is nearly free once the first has populated the build cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/manager ./cmd/manager && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/scheduler ./cmd/scheduler

# The out-of-tree scheduler-framework binary: it registers the "jobtree" plugin —
# the sole committer of GPU funding — and runs it for the schedulerName: jobtree
# profile.
FROM gcr.io/distroless/static-debian12:nonroot AS scheduler
WORKDIR /
COPY --from=build /out/scheduler /scheduler
USER 65532:65532
ENTRYPOINT ["/scheduler"]

# The controller manager. LAST on purpose: a bare `docker build .` with no
# --target still produces the manager image, as it did before this file grew a
# second target.
FROM gcr.io/distroless/static-debian12:nonroot AS manager
WORKDIR /
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
