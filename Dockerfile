# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
# CGO_ENABLED=0 is safe here: modernc.org/sqlite is a pure-Go SQLite driver
# (design.md §6), so cross-compiling for amd64/arm64 needs no C toolchain.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/bpm ./cmd/bpm

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/bpm /bpm
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/bpm", "serve", "--db", "/data/bpm.db", "--addr", ":8080"]
