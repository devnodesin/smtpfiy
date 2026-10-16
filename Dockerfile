# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/smtpify ./cmd/smtpify

# ---- runtime stage ----
FROM alpine:3.20
RUN addgroup -S -g 1000 smtpify && adduser -S -u 1000 -G smtpify smtpify
WORKDIR /smtpfiy
COPY --from=build /out/smtpify /usr/local/bin/smtpify
USER smtpify

# SMTP (2525) and HTTP (7001) ports.
EXPOSE 2525 7001

# Persist queue, logs, and config via mounted volumes.
VOLUME ["/smtpfiy/data", "/smtpfiy/log", "/smtpfiy/config"]

ENTRYPOINT ["/usr/local/bin/smtpify"]
CMD ["-c", "/smtpfiy/config/smtpfiy.yaml"]
