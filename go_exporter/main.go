package main

// This Go exporter provides a Prometheus metrics endpoint and a small log
// endpoint for the Arris SB8200 cable modem.  It logs into the modem,
// fetches XML data from the modem’s internal API and exposes status,
// downstream/upstream channel metrics, configuration parameters and event
// counts.  All configuration can be set via environment variables so it
// behaves sensibly inside Docker or Kubernetes.  See the accompanying
// README for detailed usage instructions.

import (
    "bytes"
    "encoding/xml"
    "fmt"
    "io/ioutil"
    "log"
    "net/http"
    "net/http/cookiejar"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// -----------------------------------------------------------------------------
// Configuration and environment helpers
//
// The exporter can be customised entirely via environment variables.  The
// following variables are recognised:
//   SB8200_HOST          – hostname or IP of the modem (default 192.168.100.1)
//   SB8200_USER          – username for modem login (default admin)
//   SB8200_PASSWORD      – password for modem login (required – no default)
//   SB8200_PORT          – local port on which to expose the exporter (default 9215)
//   SB8200_POLL_INTERVAL – polling interval in seconds (default 15)
//   SB8200_LOGS_MAX      – number of log entries to keep for the /logs endpoint (default 100)
//
// When running inside Docker these values are typically supplied via the
// environment directive in docker‑compose.yml or via `-e` flags to
// `docker run`.

var (
    modemHost    = getEnv("SB8200_HOST", "192.168.100.1")
    username     = getEnv("SB8200_USER", "admin")
    password     = os.Getenv("SB8200_PASSWORD")
    listenPort   = getEnv("SB8200_PORT", "9215")
    pollInterval = getEnvInt("SB8200_POLL_INTERVAL", 15)
    maxLogs      = getEnvInt("SB8200_LOGS_MAX", 100)
)

func getEnv(key, def string) string {
    val := os.Getenv(key)
    if val == "" {
        return def
    }
    return val
}

func getEnvInt(key string, def int) int {
    val := os.Getenv(key)
    if val == "" {
        return def
    }
    i, err := strconv.Atoi(val)
    if err != nil {
        return def
    }
    return i
}

// -----------------------------------------------------------------------------
// XML response structures
//
// The modem returns XML for various "fun" values.  A handful of structs are
// defined here to unmarshal the relevant parts of those responses.  Should
// Arris change their firmware or add/remove elements these structs may need
// adjustment.  Fields not required by the exporter are omitted.

// StatusResponse holds high‑level modem status.  fun=1
type StatusResponse struct {
    XMLName        xml.Name `xml:"data"`
    CMStatus       string   `xml:"cm_status"`
    CMSystemUptime string   `xml:"cm_system_uptime"`
    SwVersion      string   `xml:"SwVersion"`
    PrimaryFreq    string   `xml:"freq"`
    PrimaryPow     string   `xml:"pow"`
    PrimarySnr     string   `xml:"snr"`
}

// DownstreamChannel represents one bonded downstream channel.  fun=16
// Field names reflect the XML keys used by the modem’s API.  Some
// implementations may differ; adjust accordingly if your modem returns
// different element names.
type DownstreamChannel struct {
    ChannelID       string `xml:"id"`
    LockStatus      string `xml:"lock"`
    Modulation      string `xml:"mod"`
    Frequency       string `xml:"freq"`
    Power           string `xml:"pow"`
    SNR             string `xml:"snr"`
    Correcteds      string `xml:"correcteds"`
    Uncorrectables  string `xml:"uncorrectables"`
}

// DownstreamResponse holds the downstream channel table.  fun=16
type DownstreamResponse struct {
    Channels []DownstreamChannel `xml:"chnl"`
}

// UpstreamChannel represents one bonded upstream channel.  fun=18
type UpstreamChannel struct {
    ChannelID  string `xml:"id"`
    LockStatus string `xml:"lock"`
    Modulation string `xml:"mod"`
    Frequency  string `xml:"freq"`
    Power      string `xml:"pow"`
}

// UpstreamResponse holds the upstream channel table.  fun=18
type UpstreamResponse struct {
    Channels []UpstreamChannel `xml:"chnl"`
}

// ConfigResponse holds selected configuration values.  fun=8
type ConfigResponse struct {
    XMLName     xml.Name `xml:"data"`
    ChannelPlan string   `xml:"ChannelPlan"`
    LEDControl  string   `xml:"LEDControl"`
    EeePortState string  `xml:"EeePortState"`
}

// EventEntry represents one entry in the modem’s event log.  fun=20
type EventEntry struct {
    ID    string `xml:"id"`
    Time  string `xml:"time"`
    Level string `xml:"level"`
    Desc  string `xml:"desc"`
}

// EventLogResponse wraps the event log list.  fun=20
type EventLogResponse struct {
    LogNum   int          `xml:"log_num"`
    EventLog []EventEntry `xml:"eventlog"`
}

// -----------------------------------------------------------------------------
// Prometheus metrics definitions

var (
    // upMetric indicates whether the last scrape was successful (1) or not (0).
    upMetric = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_modem_up",
        Help: "Whether the last scrape of the modem was successful (1) or failed (0)",
    })

    downstreamFreq = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_downstream_frequency_hz",
        Help: "Primary downstream frequency in Hz",
    })
    downstreamPower = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_downstream_power_dbmv",
        Help: "Primary downstream power in dBmV",
    })
    downstreamSnr = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_downstream_snr_db",
        Help: "Primary downstream SNR in dB",
    })

    channelPlan = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_channel_plan",
        Help: "Channel plan (1=North America, 2=Europe, etc)",
    })
    ledStatus = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_led_status",
        Help: "LED status (0=Off, 1=On)",
    })
    eeeState = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "sb8200_eee_state",
        Help: "Energy Efficient Ethernet port state (0=Disabled, 1=Enabled)",
    })

    // Downstream channel metrics keyed by channel ID
    downstreamChannelFreq = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_frequency_hz",
        Help: "Downstream channel frequency in Hz",
    }, []string{"channel"})
    downstreamChannelPower = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_power_dbmv",
        Help: "Downstream channel power in dBmV",
    }, []string{"channel"})
    downstreamChannelSnr = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_snr_db",
        Help: "Downstream channel SNR in dB",
    }, []string{"channel"})
    downstreamChannelLock = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_locked",
        Help: "Downstream channel lock status (1=Locked, 0=Unlocked)",
    }, []string{"channel"})
    downstreamChannelCorrected = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_corrected",
        Help: "Corrected codeword count per downstream channel",
    }, []string{"channel"})
    downstreamChannelUncorrectable = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_downstream_channel_uncorrectable",
        Help: "Uncorrectable codeword count per downstream channel",
    }, []string{"channel"})

    // Upstream channel metrics keyed by channel ID
    upstreamChannelPower = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_upstream_channel_power_dbmv",
        Help: "Upstream channel power in dBmV",
    }, []string{"channel"})
    upstreamChannelFreq = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_upstream_channel_frequency_hz",
        Help: "Upstream channel center frequency in Hz",
    }, []string{"channel"})
    upstreamChannelLock = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_upstream_channel_locked",
        Help: "Upstream channel lock status (1=Locked, 0=Unlocked)",
    }, []string{"channel"})

    // Event log counts by level (critical, warning, notice, etc)
    eventLogCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "sb8200_eventlog_count",
        Help: "Number of event log entries by severity level",
    }, []string{"level"})
)

func init() {
    prometheus.MustRegister(
        upMetric,
        downstreamFreq,
        downstreamPower,
        downstreamSnr,
        channelPlan,
        ledStatus,
        eeeState,
        downstreamChannelFreq,
        downstreamChannelPower,
        downstreamChannelSnr,
        downstreamChannelLock,
        downstreamChannelCorrected,
        downstreamChannelUncorrectable,
        upstreamChannelPower,
        upstreamChannelFreq,
        upstreamChannelLock,
        eventLogCount,
    )
}

// -----------------------------------------------------------------------------
// HTTP client and helper routines

var client *http.Client
var recentLogs []EventEntry

// login performs the SOAP login sequence required before calling any
// authenticated endpoints.  The modem sets a session cookie on success.
func login() error {
    loginXML := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <Login>
      <Username>%s</Username>
      <LoginPassword>%s</LoginPassword>
    </Login>
  </soap:Body>
</soap:Envelope>`, username, password)
    url := fmt.Sprintf("http://%s/xml/login.xml", modemHost)
    req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(loginXML)))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "text/xml")
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        body, _ := ioutil.ReadAll(resp.Body)
        return fmt.Errorf("login failed: %s", string(body))
    }
    return nil
}

// fetchXML posts a small form payload to the modem’s getter endpoint.  The
// payload is typically "fun=n" where n selects the type of data returned.
func fetchXML(payload string) ([]byte, error) {
    url := fmt.Sprintf("http://%s/xml/getter.xml", modemHost)
    req, err := http.NewRequest("POST", url, bytes.NewBufferString(payload))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    return ioutil.ReadAll(resp.Body)
}

// parseFloat safely converts a string to float64.  Empty or invalid strings
// evaluate to 0.0.
func parseFloat(s string) float64 {
    if s == "" {
        return 0.0
    }
    // Remove any non‑numeric suffixes (e.g. dBmV, Hz) if present
    s = strings.TrimSpace(s)
    s = strings.TrimSuffix(s, "dBmV")
    s = strings.TrimSuffix(s, "dB")
    s = strings.TrimSuffix(s, "Hz")
    f, err := strconv.ParseFloat(s, 64)
    if err != nil {
        return 0.0
    }
    return f
}

// statusToFloat converts lock/status strings to 1 or 0.  Recognises
// "locked", "1", "on" as true.
func statusToFloat(s string) float64 {
    s = strings.TrimSpace(strings.ToLower(s))
    switch s {
    case "1", "true", "locked", "yes", "on":
        return 1.0
    default:
        return 0.0
    }
}

// saveRecentLogs truncates the global log buffer to maxLogs entries.
func saveRecentLogs(logs []EventEntry) {
    if len(logs) > maxLogs {
        recentLogs = logs[len(logs)-maxLogs:]
    } else {
        recentLogs = logs
    }
}

// logsHandler serves the most recent event log entries as plain text.  The
// optional query parameter ?count=n restricts the number of lines returned.
func logsHandler(w http.ResponseWriter, r *http.Request) {
    n := maxLogs
    if val := r.URL.Query().Get("count"); val != "" {
        if i, err := strconv.Atoi(val); err == nil && i > 0 && i < n {
            n = i
        }
    }
    // Determine start index based on requested count
    start := 0
    if len(recentLogs) > n {
        start = len(recentLogs) - n
    }
    for _, entry := range recentLogs[start:] {
        fmt.Fprintf(w, "%s [%s] %s\n", entry.Time, entry.Level, entry.Desc)
    }
}

// updateMetrics logs into the modem, fetches all relevant XML documents,
// updates the Prometheus metrics and stores event logs.
func updateMetrics() {
    if password == "" {
        log.Println("SB8200_PASSWORD is not set; skipping scrape")
        upMetric.Set(0)
        return
    }
    if err := login(); err != nil {
        log.Printf("Modem login failed: %v", err)
        upMetric.Set(0)
        return
    }
    upMetric.Set(1)
    // Status values
    raw, err := fetchXML("fun=1")
    if err == nil {
        var statusResp StatusResponse
        if err := xml.Unmarshal(raw, &statusResp); err == nil {
            downstreamFreq.Set(parseFloat(statusResp.PrimaryFreq))
            downstreamPower.Set(parseFloat(statusResp.PrimaryPow))
            downstreamSnr.Set(parseFloat(statusResp.PrimarySnr))
        }
    }
    // Configuration values
    raw, err = fetchXML("fun=8")
    if err == nil {
        var cfgResp ConfigResponse
        if err := xml.Unmarshal(raw, &cfgResp); err == nil {
            channelPlan.Set(parseFloat(cfgResp.ChannelPlan))
            ledStatus.Set(parseFloat(cfgResp.LEDControl))
            eeeState.Set(parseFloat(cfgResp.EeePortState))
        }
    }
    // Downstream channels
    raw, err = fetchXML("fun=16")
    if err == nil {
        var dsResp DownstreamResponse
        if err := xml.Unmarshal(raw, &dsResp); err == nil {
            for _, ch := range dsResp.Channels {
                id := ch.ChannelID
                downstreamChannelFreq.WithLabelValues(id).Set(parseFloat(ch.Frequency))
                downstreamChannelPower.WithLabelValues(id).Set(parseFloat(ch.Power))
                downstreamChannelSnr.WithLabelValues(id).Set(parseFloat(ch.SNR))
                downstreamChannelLock.WithLabelValues(id).Set(statusToFloat(ch.LockStatus))
                downstreamChannelCorrected.WithLabelValues(id).Set(parseFloat(ch.Correcteds))
                downstreamChannelUncorrectable.WithLabelValues(id).Set(parseFloat(ch.Uncorrectables))
            }
        }
    }
    // Upstream channels
    raw, err = fetchXML("fun=18")
    if err == nil {
        var usResp UpstreamResponse
        if err := xml.Unmarshal(raw, &usResp); err == nil {
            for _, ch := range usResp.Channels {
                id := ch.ChannelID
                upstreamChannelPower.WithLabelValues(id).Set(parseFloat(ch.Power))
                upstreamChannelFreq.WithLabelValues(id).Set(parseFloat(ch.Frequency))
                upstreamChannelLock.WithLabelValues(id).Set(statusToFloat(ch.LockStatus))
            }
        }
    }
    // Event log and counts
    raw, err = fetchXML("fun=20")
    if err == nil {
        var logResp EventLogResponse
        if err := xml.Unmarshal(raw, &logResp); err == nil {
            counts := map[string]int{}
            for _, entry := range logResp.EventLog {
                lvl := strings.ToLower(entry.Level)
                counts[lvl]++
            }
            eventLogCount.Reset()
            for lvl, c := range counts {
                eventLogCount.WithLabelValues(lvl).Set(float64(c))
            }
            saveRecentLogs(logResp.EventLog)
        }
    }
}

// -----------------------------------------------------------------------------
// main initialises the HTTP client and schedules the polling loop.  It
// registers the Prometheus handler and the log endpoint then starts the
// webserver.  The exporter exits fatally if the server cannot be started.
func main() {
    jar, _ := cookiejar.New(nil)
    client = &http.Client{
        Timeout: 10 * time.Second,
        Jar:     jar,
    }
    // Periodic polling loop
    go func() {
        for {
            updateMetrics()
            time.Sleep(time.Duration(pollInterval) * time.Second)
        }
    }()
    http.Handle("/metrics", promhttp.Handler())
    http.HandleFunc("/logs", logsHandler)
    log.Printf("Listening on :%s for SB8200 exporter", listenPort)
    log.Fatal(http.ListenAndServe(":"+listenPort, nil))
}