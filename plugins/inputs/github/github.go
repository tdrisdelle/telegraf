package github

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

// Github struct
type Github struct {
	Name            string
	StatsURI        string
	Method          string
	TagKeys         []string
	ResponseTimeout internal.Duration

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
  interval = "1h"

  statsURI = "https://api.github.com/users/YOUR_ORG_ID/repos"
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"

  ## List of tag names to extract from top-level of JSON server response
  tag_keys = [
    "id",
  ]
  
  fieldpass = ["forks", "open_issues", "watchers", "html_url", "name", "stargazers_count"]
  
`

func (g *Github) SampleConfig() string {
	return sampleConfig
}

func (g *Github) Description() string {
	return "Read flattened metrics from one or more JSON HTTP endpoints"
}

// Gathers data for all videos in a playlist.
func (g *Github) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if g.client.HTTPClient() == nil {
		tr := &http.Transport{
			ResponseHeaderTimeout: g.ResponseTimeout.Duration,
		}
		client := &http.Client{
			Transport: tr,
			Timeout:   g.ResponseTimeout.Duration,
		}
		g.client.SetHTTPClient(client)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		acc.AddError(g.gatherTopStats(acc))
	}()

	wg.Wait()

	return nil
}

// Gathers data from a github stats endpoint about repos at the top level
// Parameters:
//     acc      : The telegraf Accumulator to use
//
// Returns:
//     error: Any error that may have occurred
func (g *Github) gatherTopStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := g.sendRequest(g.StatsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "github"
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, g.TagKeys, tags)
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
func (g *Github) sendRequest(serverURL string) (string, float64, error) {
	// Prepare URL
	requestURL, err := url.Parse(serverURL)
	if err != nil {
		return "", -1, fmt.Errorf("Invalid server URL \"%s\"", serverURL)
	}

	data := url.Values{}
	params := requestURL.Query()
	requestURL.RawQuery = params.Encode()

	// Create + send request
	req, err := http.NewRequest(g.Method, requestURL.String(),
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", -1, err
	}

	start := time.Now()
	resp, err := g.client.MakeRequest(req)
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
	inputs.Add("github", func() telegraf.Input {
		return &Github{
			client: &RealHTTPClient{},
			ResponseTimeout: internal.Duration{
				Duration: 5 * time.Second,
			},
		}
	})
}
