package youtube

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers"
)

var (
	utf8BOM = []byte("\xef\xbb\xbf")
)

// YouTube struct
type YouTube struct {
	Name               string
	PlaylistItemsURI   string
	VideoStatisticsURI string
	ApiKey             string
	Method             string
	TagKeys            []string
	ResponseTimeout    internal.Duration
	Parameters         map[string]string
	Headers            map[string]string

	// Path to CA file
	SSLCA string `toml:"ssl_ca"`
	// Path to host cert file
	SSLCert string `toml:"ssl_cert"`
	// Path to cert key file
	SSLKey string `toml:"ssl_key"`
	// Use SSL but skip chain & host verification
	InsecureSkipVerify bool

	client HTTPClient
}

type HTTPClient interface {
	// Returns the result of an http request
	//
	// Parameters:
	// req: HTTP request object
	//
	// Returns:
	// http.Response:  HTTP respons object
	// error        :  Any error that may have occurred
	MakeRequest(req *http.Request) (*http.Response, error)

	SetHTTPClient(client *http.Client)
	HTTPClient() *http.Client
}

type RealHTTPClient struct {
	client *http.Client
}

func (c *RealHTTPClient) MakeRequest(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

func (c *RealHTTPClient) SetHTTPClient(client *http.Client) {
	c.client = client
}

func (c *RealHTTPClient) HTTPClient() *http.Client {
	return c.client
}

var sampleConfig = `
  ## NOTE This plugin only reads numerical measurements, strings and booleans
  ## will be ignored.

  playlistItemsURI = "https://www.googleapis.com/youtube/v3/playlistItems?part=snippet&maxResults=50&playlistId=PLbssOJyyvHuWiBQAg9EFWH570timj2fxt"
  videoStatisticsURI = "https://www.googleapis.com/youtube/v3/videos?part=statistics"

  apiKey = ""
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"

  ## HTTP method to use: GET or POST (case-sensitive)
  method = "GET"
  
  ## List of tag names to extract from top-level of JSON server response
  # tag_keys = [
  #   "my_tag_1",
  #   "my_tag_2"
  # ]

  ## HTTP parameters (all values must be strings).  For "GET" requests, data
  ## will be included in the query.  For "POST" requests, data will be included
  ## in the request body as "x-www-form-urlencoded".
  # [inputs.httpjson.parameters]
  #   event_type = "cpu_spike"
  #   threshold = "0.75"

  ## HTTP Headers (all values must be strings)
  # [inputs.youtube.headers]
  #   X-Auth-Token = "my-xauth-token"
  #   apiVersion = "v1"

  ## Optional SSL Config
  # ssl_ca = "/etc/telegraf/ca.pem"
  # ssl_cert = "/etc/telegraf/cert.pem"
  # ssl_key = "/etc/telegraf/key.pem"
  ## Use SSL but skip chain & host verification
  # insecure_skip_verify = false
  
  fieldpass = ["*_statistics_*", "*id"]
`

func (h *YouTube) SampleConfig() string {
	return sampleConfig
}

func (h *YouTube) Description() string {
	return "Read flattened metrics from one or more JSON HTTP endpoints"
}

// Gathers data for all videos in a playlist.
func (h *YouTube) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if h.client.HTTPClient() == nil {
		tlsCfg, err := internal.GetTLSConfig(
			h.SSLCert, h.SSLKey, h.SSLCA, h.InsecureSkipVerify)
		if err != nil {
			return err
		}
		tr := &http.Transport{
			ResponseHeaderTimeout: h.ResponseTimeout.Duration,
			TLSClientConfig:       tlsCfg,
		}
		client := &http.Client{
			Transport: tr,
			Timeout:   h.ResponseTimeout.Duration,
		}
		h.client.SetHTTPClient(client)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		acc.AddError(h.gatherPlaylist(acc))
	}()

	wg.Wait()

	return nil
}

// Gathers data from a youtube endpoints for videos in a playlist
// Parameters:
//     acc      : The telegraf Accumulator to use
//
// Returns:
//     error: Any error that may have occurred
func (h *YouTube) gatherPlaylist(
	acc telegraf.Accumulator,
) error {
	resp, _, err := h.sendRequest(h.PlaylistItemsURI + "&key=" + h.ApiKey)
	if err != nil {
		return err
	}

	var msrmnt_name string
	if h.Name == "" {
		msrmnt_name = "youtube"
	} else {
		msrmnt_name = "youtube_" + h.Name
	}
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, h.TagKeys, tags)
	if err != nil {
		return err
	}

	playlistItemsMetrics, err := parser.Parse([]byte(resp))
	if err != nil {
		return err
	}

	// iterate through the metric items in the playlist, extract their videoId
	// and then request the stats for that video
	for k, item := range playlistItemsMetrics[0].Fields() {
		if !strings.HasSuffix(k, "videoId") {
			continue
		}

		videoId := item.(string)
		// get stats
		resp, _, err := h.sendRequest(h.VideoStatisticsURI + "&id=" + videoId + "&key=" + h.ApiKey)
		if err != nil {
			return err
		}

		metrics, err := parser.Parse([]byte(resp))
		if err != nil {
			return err
		}

		for _, metric := range metrics {
			fields := make(map[string]interface{})
			for k, v := range metric.Fields() {
				// statistic counts are coming through as strings, so
				// force the issue!
				if strings.HasSuffix(k, "Count") {
					f, err := strconv.ParseFloat(v.(string), 64)
					if err != nil {
						return err
					}
					fields[k] = f
				} else {
					fields[k] = v
				}
			}
			acc.AddFields(metric.Name(), fields, metric.Tags())
		}
	}

	return nil
}

// Sends an HTTP request to the server using the HttpJson object's HTTPClient.
// This request can be either a GET or a POST.
// Parameters:
//     serverURL: endpoint to send request to
//
// Returns:
//     string: body of the response
//     error : Any error that may have occurred
func (h *YouTube) sendRequest(serverURL string) (string, float64, error) {
	// Prepare URL
	requestURL, err := url.Parse(serverURL)
	if err != nil {
		return "", -1, fmt.Errorf("Invalid server URL \"%s\"", serverURL)
	}

	data := url.Values{}
	switch {
	case h.Method == "GET":
		params := requestURL.Query()
		for k, v := range h.Parameters {
			params.Add(k, v)
		}
		requestURL.RawQuery = params.Encode()

	case h.Method == "POST":
		requestURL.RawQuery = ""
		for k, v := range h.Parameters {
			data.Add(k, v)
		}
	}

	// Create + send request
	req, err := http.NewRequest(h.Method, requestURL.String(),
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", -1, err
	}

	// Add header parameters
	for k, v := range h.Headers {
		if strings.ToLower(k) == "host" {
			req.Host = v
		} else {
			req.Header.Add(k, v)
		}
	}

	start := time.Now()
	resp, err := h.client.MakeRequest(req)
	if err != nil {
		return "", -1, err
	}

	defer resp.Body.Close()
	responseTime := time.Since(start).Seconds()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return string(body), responseTime, err
	}
	body = bytes.TrimPrefix(body, utf8BOM)

	// Process response
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("Response from url \"%s\" has status code %d (%s), expected %d (%s)",
			requestURL.String(),
			resp.StatusCode,
			http.StatusText(resp.StatusCode),
			http.StatusOK,
			http.StatusText(http.StatusOK))
		return string(body), responseTime, err
	}

	return string(body), responseTime, err
}

func init() {
	inputs.Add("youtube", func() telegraf.Input {
		return &YouTube{
			client: &RealHTTPClient{},
			ResponseTimeout: internal.Duration{
				Duration: 5 * time.Second,
			},
		}
	})
}
