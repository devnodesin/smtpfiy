# syntax=docker/dockerfile:1

# Build base image MUST match (or exceed) the `go` directive in go.mod.
# go.mod currently requires go >= 1.26.0.
# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/smtpfiy ./cmd/smtpfiy

# ---- runtime stage ----
FROM alpine:3.20
RUN addgroup -S -g 1000 smtpfiy && adduser -S -u 1000 -G smtpfiy smtpfiy
WORKDIR /smtpfiy
COPY --from=build /out/smtpfiy /usr/local/bin/smtpfiy
USER smtpfiy

# SMTP (2525) and HTTP (7001) ports.
EXPOSE 2525 7001

# Persist queue, logs, and config via mounted volumes.
VOLUME ["/smtpfiy/data", "/smtpfiy/log", "/smtpfiy/config"]

# Docker healthcheck per SPEC §9/§11: call GET /health. BusyBox wget
# (ships with alpine) returns non-zero on HTTP failure.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O- http://localhost:7001/health || exit 1

ENTRYPOINT ["/usr/local/bin/smtpfiy"]
CMD ["-c", "/smtpfiy/config/smtpfiy.yaml"]
