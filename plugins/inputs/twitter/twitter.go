package twitter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

var (
	utf8BOM = []byte("\xef\xbb\xbf")
)

// Twitter struct
type Twitter struct {
	Name           string
	ConsumerSecret string
	ConsumerKey    string
	TokenURL       string
	ScreenNames    []string
	TagKeys        []string

	client TwitterClient
}

type TwitterClient interface {
	UsersShow(screenName string) (*twitter.User, *http.Response, error)

	SetTwitterClient(c *twitter.Client)
	TwitterClient() *twitter.Client
}

type RealTwitterClient struct {
	client *twitter.Client
}

func (c *RealTwitterClient) UsersShow(screenName string) (*twitter.User, *http.Response, error) {
	userShowParams := &twitter.UserShowParams{ScreenName: screenName}
	u, r, e := c.client.Users.Show(userShowParams)
	fmt.Printf("USERS SHOW:\n%+v\n", u)
	return u, r, e
}

func (c *RealTwitterClient) SetTwitterClient(client *twitter.Client) {
	c.client = client
}

func (c *RealTwitterClient) TwitterClient() *twitter.Client {
	return c.client
}

func NewHTTPClient(consumerKey string, consumerSecret string, tokenURL string) *http.Client {
	config := &clientcredentials.Config{
		ClientID:     consumerKey,
		ClientSecret: consumerSecret,
		TokenURL:     tokenURL,
	}
	return config.Client(oauth2.NoContext)
}

var sampleConfig = `
  ## NOTE This plugin only reads numerical measurements, strings and booleans
  ## will be ignored.
    
  consumerKey = "DXGB3b8cCeqzpiauqGwN9hgEn"
  consumerSecret = "nLdQsLS1FOmVOoJFZy1XSRyzqEM8osNZpIBaeTXrc0QQCEb7lk"

  ## Twitter Token URL endpoint
  tokenURL = "https://api.twitter.com/oauth2/token"
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"

  screenNames = [
  	"thecodeteam"
  ]

  ## List of tag names to extract from top-level of JSON server response
  # tag_keys = [
  #   "followers_count",
  #   "friends_count",
  #	  "listed_count",
  #	  "favourites_count"
  #   "statuses_count"
  # ]

  ## Parameters (all values must be strings).
  # [inputs.twitter.parameters]
  #   screenname = "kubecon"
  #   count = 2

`

func (t *Twitter) SampleConfig() string {
	return sampleConfig
}

func (t *Twitter) Description() string {
	return "Read flattened metrics from Twitter API endpoints"
}

// Gathers data for all servers.
func (t *Twitter) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	fmt.Printf("Gathering time...\n")
	if t.client.TwitterClient() == nil {
		fmt.Printf("...no client, so create new one\n")

		httpClient := NewHTTPClient(t.ConsumerKey, t.ConsumerSecret, t.TokenURL)

		client := twitter.NewClient(httpClient)

		t.client.SetTwitterClient(client)
	}

	fmt.Printf("Iterate through screen names...\n")
	for _, screenname := range t.ScreenNames {
		wg.Add(1)
		go func(screenname string) {
			defer wg.Done()
			acc.AddError(t.gatherScreenName(acc, screenname))
		}(screenname)
	}
	fmt.Printf("Finished screen names\n")

	wg.Wait()

	return nil
}

// Gathers data for a particular screenname
// Parameters:
//     acc      : The telegraf Accumulator to use
//	   screenName : screen name to be queried
//
// Returns:
//     error: Any error that may have occurred
func (t *Twitter) gatherScreenName(
	acc telegraf.Accumulator,
	screenName string,
) error {
	user, responseTime, err := t.showUser(screenName)
	if err != nil {
		return err
	}

	var msrmnt_name string
	if t.Name == "" {
		msrmnt_name = "twitter"
	} else {
		msrmnt_name = "twitter_" + t.Name
	}
	tags := map[string]string{
		"screenname": screenName,
	}

	parser, err := parsers.NewJSONParser(msrmnt_name, t.TagKeys, tags)
	if err != nil {
		return err
	}

	b, err := json.Marshal(user)
	if err != nil {
		return err
	}
	fmt.Printf("raw user:\n%+v\n", string(b))

	metrics, err := parser.Parse(b)
	if err != nil {
		return err
	}
	fmt.Printf("parsed user:\n%+v\n", metrics)

	for _, metric := range metrics {
		fields := make(map[string]interface{})
		for k, v := range metric.Fields() {
			fields[k] = v
		}
		fields["response_time"] = responseTime
		acc.AddFields(metric.Name(), fields, metric.Tags())
	}

	return nil
}

func (t *Twitter) showUser(screenName string) (*twitter.User, float64, error) {
	start := time.Now()
	user, _, err := t.client.UsersShow(screenName)
	responseTime := time.Since(start).Seconds()

	if err != nil {
		return nil, -1, fmt.Errorf("Invalid screen name \"%s\"", screenName)
	}

	return user, responseTime, err
}

func init() {
	inputs.Add("twitter", func() telegraf.Input {
		return &Twitter{
			client: &RealTwitterClient{},
		}
	})
}
