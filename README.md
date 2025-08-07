# SB8200 Prometheus Exporter

A simple Prometheus exporter for ARRIS SB8200 (and similar) cable modems.  
Exports downstream/upstream channel metrics, signal statistics, error counters and basic modem status fields.  
The exporter logs into the modem using its web interface, fetches relevant XML endpoints and exposes the metrics in Prometheus text format.

---

## Features

- Authenticates against the modem’s web interface (requires admin password).
- Collects downstream and upstream bonded channel data: power, SNR, lock status and error counts.
- Exposes high‑level status such as system uptime and DOCSIS mode.
- Can be run natively or inside Docker and is suitable for Kubernetes deployments.

---

## Usage

This repository contains **two independent exporters** that scrape
metrics from the Arris SB8200 modem and expose them to Prometheus:

1. **Python/Flask exporter** – the original implementation which uses
   Flask to serve metrics on port 9800.  Environment variables begin
   with `SB8200_URL`, `SB8200_USER` and `SB8200_PASS`.  This is the default
   when using the root `Dockerfile` and `docker-compose.yml`.
2. **Go exporter (optional)** – a self‑contained Go binary which uses
   the Prometheus client library directly and serves metrics on port
   9215.  Configuration is via environment variables prefixed with
   `SB8200_`.  This implementation lives under
   [`go_exporter/`](./go_exporter) and can be built/run separately.  It
   exposes an additional `/logs` endpoint showing recent event log
   entries.

### Running the Python exporter via Docker

To run the exporter via Docker, set the modem credentials as environment variables and map the port:

```bash
docker run -d \
  -e SB8200_PASS='your_modem_admin_password' \
  -e SB8200_URL='http://192.168.100.1' \
  -e SB8200_USER='Admin' \
  -p 9800:9800 \
  chaoscorp/sb8200-exporter:latest
```

**Environment variables for the Python exporter:**

| Variable        | Description                              | Default                |
|-----------------|------------------------------------------|------------------------|
| `SB8200_URL`    | Base URL of the modem web interface       | `http://192.168.100.1`|
| `SB8200_USER`   | Username for modem login                  | `Admin`               |
| `SB8200_PASS`   | Password for modem login (required)       | –                     |

Once running, Prometheus can scrape metrics from `http://<host>:9800/metrics`.

### Running the Go exporter via Docker

To use the Go implementation, change into the `go_exporter` directory and
build or run the image.  The Go exporter listens on port **9215** by
default and exposes both `/metrics` and `/logs` endpoints.

```bash
cd go_exporter
# build the image
docker build -t youruser/sb8200-exporter-go .
# run the container with your modem credentials
docker run -d \
  -e SB8200_PASSWORD='your_modem_admin_password' \
  -e SB8200_HOST='192.168.100.1' \
  -e SB8200_USER='admin' \
  -p 9215:9215 \
  youruser/sb8200-exporter-go
```

Alternatively, use the provided `docker-compose.yml` in the
`go_exporter/` directory to start the service.  Only `SB8200_PASSWORD` is
required; all other variables have sensible defaults.  You can override
any of them by supplying your own environment variables.

**Environment variables for the Go exporter:**

| Variable                | Description                                                  | Default          |
|------------------------|--------------------------------------------------------------|------------------|
| `SB8200_HOST`          | IP or hostname of the modem                                  | `192.168.100.1` |
| `SB8200_USER`          | Username for modem login                                     | `admin`         |
| `SB8200_PASSWORD`      | Password for modem login (**required**)                      | –               |
| `SB8200_PORT`          | Port that the exporter listens on                            | `9215`          |
| `SB8200_POLL_INTERVAL` | Interval in seconds between scrapes                          | `15`            |
| `SB8200_LOGS_MAX`      | Maximum number of event log entries returned by `/logs`      | `100`           |

Once running, Prometheus can scrape metrics from
`http://<host>:9215/metrics` and fetch the last N event log entries from
`http://<host>:9215/logs?count=N`.

### Running locally

Install dependencies and run the Flask application directly:

```bash
pip install -r requirements.txt
export SB8200_PASS='your_modem_admin_password'
python sb8200_exporter.py
```

To run the Go exporter directly without Docker you will need Go 1.22 or newer.
Change into the `go_exporter` directory and run the code.  The same
environment variables described above are honoured.

```bash
cd go_exporter
go run main.go
```

---

## Prometheus configuration

Add a scrape job to your Prometheus configuration.  When using the Python
exporter the default port is 9800; if you use the Go exporter change
the port to 9215:

```yaml
scrape_configs:
  - job_name: 'sb8200'
    static_configs:
      - targets: ['<exporter-ip>:9800']
```

---

## Development

This repository contains both a Python/Flask application and a Go
implementation, along with Dockerfiles and docker‑compose manifests for
each.  Contributions and bug reports are welcome.

---

## Security

This exporter logs into your modem using its administrative credentials.  
Do **not** expose the exporter or your modem’s web interface directly to the internet.  
Keep the admin password secret and consider running the exporter in a trusted environment only.

---

## License

MIT