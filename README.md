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

### Running via Docker

To run the exporter via Docker, set the modem credentials as environment variables and map the port:

```bash
docker run -d \
  -e SB8200_PASS='your_modem_admin_password' \
  -e SB8200_URL='http://192.168.100.1' \
  -e SB8200_USER='Admin' \
  -p 9800:9800 \
  chaoscorp/sb8200-exporter:latest
```

**Environment variables:**

| Variable        | Description                              | Default                |
|-----------------|------------------------------------------|------------------------|
| `SB8200_URL`    | Base URL of the modem web interface       | `http://192.168.100.1`|
| `SB8200_USER`   | Username for modem login                  | `Admin`               |
| `SB8200_PASS`   | Password for modem login (required)       | –                     |

Once running, Prometheus can scrape metrics from `http://<host>:9800/metrics`.

### Running locally

Install dependencies and run the Flask application directly:

```bash
pip install -r requirements.txt
export SB8200_PASS='your_modem_admin_password'
python sb8200_exporter.py
```

---

## Prometheus configuration

Add a scrape job to your Prometheus configuration:

```yaml
scrape_configs:
  - job_name: 'sb8200'
    static_configs:
      - targets: ['<exporter-ip>:9800']
```

---

## Development

This repository contains a simple Flask application and a Dockerfile.  
Contributions and bug reports are welcome.

---

## Security

This exporter logs into your modem using its administrative credentials.  
Do **not** expose the exporter or your modem’s web interface directly to the internet.  
Keep the admin password secret and consider running the exporter in a trusted environment only.

---

## License

MIT