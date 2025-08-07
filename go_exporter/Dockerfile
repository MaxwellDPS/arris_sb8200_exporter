FROM golang:1.22-alpine AS builder

# Build the SB8200 Go exporter.  A multi‑stage build is used to keep the final
# image small and contain only the compiled binary.  The builder stage
# initialises a small Go module on the fly, fetches dependencies and
# compiles the executable.

WORKDIR /src
COPY main.go .

RUN go mod init sb8200_exporter_go \
    && go get github.com/prometheus/client_golang/prometheus \
    && go build -o /bin/sb8200_exporter main.go

FROM alpine:latest
COPY --from=builder /bin/sb8200_exporter /bin/sb8200_exporter

# Expose the port used by the exporter.  It can be overridden via
# SB8200_PORT at runtime.
EXPOSE 9215

ENTRYPOINT ["/bin/sb8200_exporter"]