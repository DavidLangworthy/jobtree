# Builds the jobtree controller manager (cmd/manager) for the kind e2e
# harness (Track F — TESTINFRA-2, docs/project/make-it-real-plan.md) and for
# real deployment via deploy/helm/gpu-fleet (values.yaml's controller.image).
#
# Multi-stage: build with the full Go toolchain, ship a static binary on a
# distroless base with no shell — matches the "no wildcard RBAC / minimal
# attack surface" discipline the rest of the chart already holds to (R22).
FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
