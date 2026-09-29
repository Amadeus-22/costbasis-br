# syntax=docker/dockerfile:1

# --- build: static binary, no cgo (modernc.org/sqlite is pure Go) ---
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/costbasis ./cmd/costbasis \
 && mkdir -p /out/data

# --- run: distroless static, non-root; zone data is embedded in the binary ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/costbasis /usr/local/bin/costbasis
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
ENV COSTBASIS_DB=/data/costbasis.db \
    COSTBASIS_HTTP_ADDR=0.0.0.0:8080
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/costbasis"]
CMD ["serve"]
