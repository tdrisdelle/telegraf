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
	OrgId           string
	GetContributors bool
	TagKeys         []string
	Method          string
	ResponseTimeout internal.Duration
	Parameters      map[string]string
	Headers         map[string]string

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
  interval = "15m"

  org_id = "YOUR_ORG_ID"
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"
  
  get_contributors = true

  ## List of tag names to extract from top-level of JSON server response
  tag_keys = [
    "id",
  ]
	 
  fieldpass = ["forks", "open_issues", "watchers", "stargazers_count", "html_url", "name", "login", "contributions"]
  
  ## HTTP Headers (all values must be strings)
  [inputs.github.headers]
     Authorization = "token YOUR_AUTH_TOKEN"
  
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
	statsURI := "https://api.github.com/users/" + g.OrgId + "/repos"
	resp, _, err := g.sendRequest(statsURI)
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

		if g.GetContributors && metric.HasTag("name") && metric.HasTag("id") {
			var repo_name string
			var repo_id string
			for k, v := range metric.Tags() {
				if k == "name" {
					repo_name = v
				} else if k == "id" {
					repo_id = v
				}
			}
			acc.AddError(g.gatherRepoContributors(acc, repo_name, repo_id))
		}
	}

	return nil
}

// Gathers data from a github stats endpoint about repo contributors
// Parameters:
//     acc      : The telegraf Accumulator to use
//	   repo		: Repo name to get contributors for
//
// Returns:
//     error: Any error that may have occurred
func (g *Github) gatherRepoContributors(
	acc telegraf.Accumulator,
	repo_name string,
	repo_id string,
) error {
	repoContributorsURI := "https://api.github.com/repos/" + g.OrgId + "/" + repo_name + "/contributors"

	resp, _, err := g.sendRequest(repoContributorsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "github_contributors"
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
		metric.AddTag("repo", repo_name)
		metric.AddTag("repo_id", repo_id)
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
	switch {
	case g.Method == "GET":
		params := requestURL.Query()
		for k, v := range g.Parameters {
			params.Add(k, v)
		}
		requestURL.RawQuery = params.Encode()

	case g.Method == "POST":
		requestURL.RawQuery = ""
		for k, v := range g.Parameters {
			data.Add(k, v)
		}
	}

	// Create + send request
	req, err := http.NewRequest(g.Method, requestURL.String(),
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", -1, err
	}

	// Add header parameters
	for k, v := range g.Headers {
		if strings.ToLower(k) == "host" {
			req.Host = v
		} else {
			req.Header.Add(k, v)
		}
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
