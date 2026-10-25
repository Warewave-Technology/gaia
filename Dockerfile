# Gaia's container image (design 13.3): make image.
FROM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN mkdir -p ../internal/web/dist && npm run build

FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/dist internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/Warewave-Technology/gaia/internal/version.Version=${VERSION}" -o /out/ ./cmd/...
# The pinned Pulumi CLI and plugins, so that runs download nothing.
RUN mkdir -p /gaia/home /gaia/cache /gaia/data && \
    HOME=/gaia/home XDG_CACHE_HOME=/gaia/cache PULUMI_HOME=/gaia/home/pulumi /out/gaia pulumi prepare

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/gaia /out/gaia-ipam /usr/bin/
COPY --from=build --chown=65532:65532 /gaia /gaia
ENV HOME=/gaia/home XDG_CACHE_HOME=/gaia/cache PULUMI_HOME=/gaia/home/pulumi PULUMI_SKIP_UPDATE_CHECK=true
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/bin/gaia"]
CMD ["server", "--listen", "0.0.0.0:8080", "--data-dir", "/gaia/data"]
