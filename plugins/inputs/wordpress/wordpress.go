package wordpress

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
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

// Wordpress struct
type Wordpress struct {
	Name                string
	TopPostsStatsURI    string
	SummaryStatsURI     string
	BearerToken         string
	Method              string
	TopPostsTagKeys     []string
	SummaryStatsTagKeys []string
	ResponseTimeout     internal.Duration
	Parameters          map[string]string
	Headers             map[string]string

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
  interval = "24h"

  topPostsStatsURI = "https://public-api.wordpress.com/rest/v1/sites/YOUR_SITE_ID/stats/top-posts?fields=top-posts"
  summaryStatsURI = "https://public-api.wordpress.com/rest/v1.1/sites/YOUR_SITE_ID/stats/summary"
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"

  ## List of tag names to extract from top-level of JSON server response
  top_posts_tag_keys = [
    "date",
	"postId",
  ]
	
  summary_stats_tag_keys = [
  ]

  ## HTTP Headers (all values must be strings)
  [inputs.wordpress.headers]
     authorization = "Bearer YOUR_BEARER_TOKEN"
  
`

func (w *Wordpress) SampleConfig() string {
	return sampleConfig
}

func (w *Wordpress) Description() string {
	return "Read flattened metrics from one or more JSON HTTP endpoints"
}

// Gathers data for all videos in a playlist.
func (w *Wordpress) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if w.client.HTTPClient() == nil {
		tlsCfg, err := internal.GetTLSConfig(
			w.SSLCert, w.SSLKey, w.SSLCA, w.InsecureSkipVerify)
		if err != nil {
			return err
		}
		tr := &http.Transport{
			ResponseHeaderTimeout: w.ResponseTimeout.Duration,
			TLSClientConfig:       tlsCfg,
		}
		client := &http.Client{
			Transport: tr,
			Timeout:   w.ResponseTimeout.Duration,
		}
		w.client.SetHTTPClient(client)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		acc.AddError(w.gatherTopPostsStats(acc))
		acc.AddError(w.gatherSummaryStats(acc))
	}()

	wg.Wait()

	return nil
}

// Gathers data from a wordpress stats endpoint about top posts
// Parameters:
//     acc      : The telegraf Accumulator to use
//
// Returns:
//     error: Any error that may have occurred
func (w *Wordpress) gatherTopPostsStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.TopPostsStatsURI)
	if err != nil {
		return err
	}
	var msrmnt_name string
	if w.Name == "" {
		msrmnt_name = "wordpress"
	} else {
		msrmnt_name = "wordpress_" + w.Name
	}
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, w.TopPostsTagKeys, tags)
	if err != nil {
		return err
	}
	resp = strings.TrimPrefix(resp, "{\"top-posts\":")
	resp = strings.TrimSuffix(resp, "}")
	metrics, err := parser.Parse([]byte(resp))
	if err != nil {
		return err
	}

	for _, metric := range metrics {
		fields := make(map[string]interface{})
		for k, v := range metric.Fields() {
			fields[k] = v
		}
		metric.AddTag("api", "top-posts")
		acc.AddFields(metric.Name(), fields, metric.Tags())
	}

	return nil
}

// Gathers data from a wordpress stats endpoint about site summary
// Parameters:
//     acc      : The telegraf Accumulator to use
//
// Returns:
//     error: Any error that may have occurred
func (w *Wordpress) gatherSummaryStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.SummaryStatsURI)
	if err != nil {
		return err
	}
	var msrmnt_name string
	if w.Name == "" {
		msrmnt_name = "wordpress"
	} else {
		msrmnt_name = "wordpress_" + w.Name
	}
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, w.SummaryStatsTagKeys, tags)
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
			fields[k] = v
		}
		metric.AddTag("api", "summary")
		acc.AddFields(metric.Name(), fields, metric.Tags())
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
func (w *Wordpress) sendRequest(serverURL string) (string, float64, error) {
	// Prepare URL
	requestURL, err := url.Parse(serverURL)
	if err != nil {
		return "", -1, fmt.Errorf("Invalid server URL \"%s\"", serverURL)
	}

	data := url.Values{}
	switch {
	case w.Method == "GET":
		params := requestURL.Query()
		for k, v := range w.Parameters {
			params.Add(k, v)
		}
		requestURL.RawQuery = params.Encode()

	case w.Method == "POST":
		requestURL.RawQuery = ""
		for k, v := range w.Parameters {
			data.Add(k, v)
		}
	}

	// Create + send request
	req, err := http.NewRequest(w.Method, requestURL.String(),
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", -1, err
	}

	// Add header parameters
	for k, v := range w.Headers {
		if strings.ToLower(k) == "host" {
			req.Host = v
		} else {
			req.Header.Add(k, v)
		}
	}

	start := time.Now()
	resp, err := w.client.MakeRequest(req)
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
	inputs.Add("wordpress", func() telegraf.Input {
		return &Wordpress{
			client: &RealHTTPClient{},
			ResponseTimeout: internal.Duration{
				Duration: 5 * time.Second,
			},
		}
	})
}
