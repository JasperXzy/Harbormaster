# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.1
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# Backend-only build: no Node, web source or generated frontend bundle required.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/hm-server ./cmd/hm-server

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/hm-server /hm-server
USER 65532:65532
ENTRYPOINT ["/hm-server"]
CMD ["serve", "--config", "/etc/harbormaster/server.json"]
