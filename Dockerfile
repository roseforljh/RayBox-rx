FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder
COPY . /src/raybox
WORKDIR /src/raybox
ARG TARGETOS TARGETARCH
ARG GOPROXY=""
ENV GOPROXY=${GOPROXY}
ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH
ENV GOTOOLCHAIN=local
RUN set -ex \
    && apk add git build-base \
    && export VERSION=$(GOOS= GOARCH= go run ./cmd/internal/read_tag) \
    && export TAGS=$(cat release/DEFAULT_BUILD_TAGS_OTHERS) \
    && export LDFLAGS_SHARED=$(cat release/LDFLAGS) \
    && go build -v -trimpath -tags "$TAGS" \
        -o /go/bin/raybox \
        -ldflags "-X \"github.com/roseforljh/RayBox-rx/constant.Version=$VERSION\" $LDFLAGS_SHARED -s -w -buildid=" \
        ./cmd/raybox
FROM --platform=$TARGETPLATFORM alpine AS dist
RUN set -ex \
    && apk add --no-cache --upgrade bash tzdata ca-certificates nftables
COPY --from=builder /go/bin/raybox /usr/local/bin/raybox
ENTRYPOINT ["raybox"]
