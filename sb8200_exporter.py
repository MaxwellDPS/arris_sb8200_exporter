import os
import requests
from flask import Flask, Response
from prometheus_client import CollectorRegistry, Gauge, generate_latest
from lxml import etree

# Configuration obtained from environment variables
MODEM_URL = os.environ.get("SB8200_URL", "http://192.168.100.1")
USERNAME = os.environ.get("SB8200_USER", "Admin")
PASSWORD = os.environ.get("SB8200_PASS")

app = Flask(__name__)


def login(session: requests.Session) -> bool:
    """Authenticate to the modem and ensure a session token is set.

    Args:
        session: requests.Session instance used for HTTP communication.

    Returns:
        True if login succeeds.

    Raises:
        Exception: if the login request fails or no session token is returned.
    """
    login_url = f"{MODEM_URL}/xml/login.xml"
    payload = {
        "Username": USERNAME,
        "LoginPassword": PASSWORD,
    }
    # Post credentials to the login endpoint.  The modem sets a sessionToken cookie on success.
    resp = session.post(login_url, data=payload, timeout=5)
    if "sessionToken" not in session.cookies:
        raise Exception("Login failed: no sessionToken in cookies")
    return True


def fetch_xml(session: requests.Session, fun_num: int) -> bytes:
    """Retrieve modem data from a specific XML endpoint.

    Args:
        session: requests.Session instance with an authenticated cookie.
        fun_num: Identifier for the data to fetch (e.g., 1 for status).

    Returns:
        Raw XML bytes returned by the modem.

    Raises:
        HTTPError: if the request fails.
    """
    url = f"{MODEM_URL}/xml/getter.xml"
    resp = session.post(url, data={"fun": fun_num}, timeout=5)
    resp.raise_for_status()
    return resp.content


def parse_status(xml: bytes) -> dict:
    """Extract high-level status information from the modem's XML response."""
    root = etree.fromstring(xml)
    data = {}
    # Tags of interest from the status endpoint.  Unknown tags will be empty.
    for tag in [
        "cm_connected",
        "HwModel",
        "cm_docsis_mode",
        "CurrentTime",
        "cm_system_uptime",
        "cm_mac_addr",
        "cm_serial_number",
        "cm_hardware_version",
        "cm_network_access",
        "cm_status",
        "BPIEnabled",
        "freq",
        "pow",
        "snr",
    ]:
        data[tag] = root.findtext(f".//{tag}", default="")
    return data


def parse_downstream(xml: bytes):
    """Iterate over downstream channel entries and yield dictionaries."""
    root = etree.fromstring(xml)
    for ds in root.findall(".//downstream"):
        yield {
            "chid": ds.findtext("chid") or ds.findtext("dsid"),
            "locked": ds.findtext("IsLocked") or ds.findtext("ofdmIsLocked"),
            "modulation": ds.findtext("mod") or ds.findtext("ofdmModulation"),
            "frequency": ds.findtext("freq") or ds.findtext("plcFrequency"),
            "power": ds.findtext("pow") or ds.findtext("PLCPower"),
            "snr": ds.findtext("snr") or ds.findtext("PlcScAvgMer"),
            "corrected": ds.findtext("correctable") or ds.findtext("ofdmCorrected") or "0",
            "uncorrectables": ds.findtext("uncorrectable") or ds.findtext("ofdmUncorrectable") or "0",
        }


def parse_upstream(xml: bytes):
    """Iterate over upstream channel entries and yield dictionaries."""
    root = etree.fromstring(xml)
    for us in root.findall(".//upstream"):
        yield {
            "usid": us.findtext("usid"),
            "locked": us.findtext("usLocked") or us.findtext("ofdmaLocked"),
            "type": "OFDMA" if us.find("ofdmaLocked") is not None else "SC-QAM",
            "frequency": us.findtext("freq") or us.findtext("centerFreq"),
            "bandwidth": us.findtext("bandwidth") or us.findtext("width"),
            "power": us.findtext("power") or us.findtext("repPower1_6"),
        }


@app.route("/metrics")
def metrics():
    """Prometheus metrics endpoint."""
    registry = CollectorRegistry()
    session = requests.Session()
    try:
        login(session)
    except Exception as e:
        return Response(f"# Modem login failed: {e}\n", status=500, mimetype="text/plain")
    # Gather status metrics
    try:
        status_xml = fetch_xml(session, 1)
        status = parse_status(status_xml)
        if status.get("cm_system_uptime"):
            uptime_str = status["cm_system_uptime"].split()[0]  # Extract numeric part before units
            try:
                uptime = float(uptime_str)
                Gauge(
                    "modem_system_uptime_seconds",
                    "System Uptime",
                    registry=registry,
                ).set(uptime)
            except ValueError:
                # If conversion fails, skip uptime metric
                pass
        # Expose DOCSIS mode as 1 if 3.1, else 0
        if "cm_docsis_mode" in status:
            Gauge(
                "modem_docsis_mode",
                "DOCSIS mode (1 for 3.1, 0 otherwise)",
                registry=registry,
            ).set(1.0 if "3.1" in status["cm_docsis_mode"] else 0.0)
    except Exception as e:
        return Response(f"# Failed to get status: {e}\n", status=500, mimetype="text/plain")
    # Downstream channels (fun=9 for OFDM, 10 for DOCSIS 3.0)
    for fun in [9, 10]:
        try:
            ds_xml = fetch_xml(session, fun)
            for ds in parse_downstream(ds_xml):
                labels = {
                    "channel": ds["chid"] if ds["chid"] else "unknown",
                    "modulation": ds["modulation"] if ds["modulation"] else "unknown",
                }
                # Downstream power
                try:
                    power = float(ds["power"] or 0)
                    Gauge(
                        "modem_downstream_power_dbmv",
                        "Downstream Power", labels.keys(), registry=registry
                    ).labels(**labels).set(power)
                except ValueError:
                    pass
                # Downstream SNR
                try:
                    snr = float(ds["snr"] or 0)
                    Gauge(
                        "modem_downstream_snr_db",
                        "Downstream SNR", labels.keys(), registry=registry
                    ).labels(**labels).set(snr)
                except ValueError:
                    pass
                # Error counters
                try:
                    corrected = int(ds["corrected"])
                    uncorrectables = int(ds["uncorrectables"])
                    Gauge(
                        "modem_downstream_corrected",
                        "Corrected Codewords", labels.keys(), registry=registry
                    ).labels(**labels).set(corrected)
                    Gauge(
                        "modem_downstream_uncorrectable",
                        "Uncorrectable Codewords", labels.keys(), registry=registry
                    ).labels(**labels).set(uncorrectables)
                except ValueError:
                    pass
                # Lock status
                locked_val = ds["locked"] or ""
                Gauge(
                    "modem_downstream_locked",
                    "Channel Locked (1 if locked)", labels.keys(), registry=registry
                ).labels(**labels).set(1.0 if locked_val.lower() in ["1", "yes", "locked"] else 0.0)
        except Exception:
            continue
    # Upstream channels (fun=6 for OFDMA, 11 for DOCSIS 3.0)
    for fun in [6, 11]:
        try:
            us_xml = fetch_xml(session, fun)
            for us in parse_upstream(us_xml):
                labels = {
                    "channel": us["usid"] if us["usid"] else "unknown",
                    "type": us["type"] if us["type"] else "unknown",
                }
                # Upstream power
                try:
                    power = float(us["power"] or 0)
                    Gauge(
                        "modem_upstream_power_dbmv",
                        "Upstream Power", labels.keys(), registry=registry
                    ).labels(**labels).set(power)
                except ValueError:
                    pass
                # Frequency
                try:
                    freq = float(us["frequency"] or 0)
                    Gauge(
                        "modem_upstream_frequency_hz",
                        "Upstream Frequency", labels.keys(), registry=registry
                    ).labels(**labels).set(freq)
                except ValueError:
                    pass
                # Lock status
                locked_val = us["locked"] or ""
                Gauge(
                    "modem_upstream_locked",
                    "Channel Locked (1 if locked)", labels.keys(), registry=registry
                ).labels(**labels).set(1.0 if locked_val.lower() in ["1", "yes", "locked"] else 0.0)
        except Exception:
            continue
    # Emit the metrics using Prometheus text format
    return Response(generate_latest(registry), mimetype="text/plain")


if __name__ == "__main__":
    # Run the Flask server.  Listen on all interfaces so Docker can expose the port.
    app.run(host="0.0.0.0", port=9800)