FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/evalvet ./cmd/evalvet && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/mockprovider ./cmd/mockprovider && \
    mkdir -p /work/out

FROM gcr.io/distroless/static-debian13:nonroot AS mockprovider
COPY --from=build /out/mockprovider /usr/local/bin/mockprovider
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mockprovider"]

FROM gcr.io/distroless/static-debian13:nonroot AS evalvet
COPY --from=build /out/evalvet /usr/local/bin/evalvet
COPY --from=build --chown=65532:65532 /work /work
WORKDIR /work
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/evalvet"]
