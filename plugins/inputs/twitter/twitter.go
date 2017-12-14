package twitter

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

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

// Bool returns a new pointer to the given bool value.
func Bool(v bool) *bool {
	ptr := new(bool)
	*ptr = v
	return ptr
}

// Twitter struct
type Twitter struct {
	Name           string
	ConsumerSecret string
	ConsumerKey    string
	TokenURL       string
	ScreenNames    []string
	TagKeys        []string
	SinceID        float64

	client TwitterClient
}

type TwitterClient interface {
	UsersShow(screenName string) (*twitter.User, *http.Response, error)
	TimelineShow(screenName string, sinceId float64) ([]twitter.Tweet, *http.Response, error)

	SetTwitterClient(c *twitter.Client)
	TwitterClient() *twitter.Client
}

type RealTwitterClient struct {
	client *twitter.Client
}

func (c *RealTwitterClient) TimelineShow(screenName string, sinceId float64) ([]twitter.Tweet, *http.Response, error) {
	var userTimelineParams *twitter.UserTimelineParams
	if sinceId > 0 {
		userTimelineParams = &twitter.UserTimelineParams{ScreenName: screenName, SinceID: int64(sinceId), TrimUser: Bool(false), ExcludeReplies: Bool(true), IncludeRetweets: Bool(true)}
	} else {
		userTimelineParams = &twitter.UserTimelineParams{ScreenName: screenName, Count: 200, TrimUser: Bool(false), ExcludeReplies: Bool(true), IncludeRetweets: Bool(true)}
	}

	ts, r, e := c.client.Timelines.UserTimeline(userTimelineParams)
	return ts, r, e
}

func (c *RealTwitterClient) UsersShow(screenName string) (*twitter.User, *http.Response, error) {
	userShowParams := &twitter.UserShowParams{ScreenName: screenName}
	u, r, e := c.client.Users.Show(userShowParams)
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
  
  screenNames = [
  	"thecodeteam",
	"biglifetechno",
  ]
  
  ## List of tag names to extract from top-level of JSON server response
  # tag_keys = [
  #   "my_tag_1",
  #   "my_tag_2"
  # ]
`

func (t *Twitter) SampleConfig() string {
	return sampleConfig
}

func (t *Twitter) Description() string {
	return "Read flattened metrics from Twitter API endpoints"
}

// Gathers data for all screen names.
func (t *Twitter) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if t.client.TwitterClient() == nil {
		httpClient := NewHTTPClient(t.ConsumerKey, t.ConsumerSecret, t.TokenURL)
		client := twitter.NewClient(httpClient)
		t.client.SetTwitterClient(client)
	}

	for _, screenname := range t.ScreenNames {
		wg.Add(1)
		go func(screenname string) {
			defer wg.Done()
			acc.AddError(t.gatherTimeline(acc, screenname, t.SinceID))
		}(screenname)
	}

	wg.Wait()

	return nil
}

func (t *Twitter) gatherTimeline(
	acc telegraf.Accumulator,
	screenName string,
	sinceId float64,
) error {
	tweets, err := t.showTimeline(screenName, sinceId)
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
		"screen_name": screenName,
	}

	parser, err := parsers.NewJSONParser(msrmnt_name, t.TagKeys, tags)
	if err != nil {
		return err
	}

	for _, tweet := range tweets {
		b, err := json.Marshal(tweet)
		if err != nil {
			return err
		}

		metrics, err := parser.Parse(b)
		if err != nil {
			return err
		}

		for _, metric := range metrics {
			fields := make(map[string]interface{})
			for k, v := range metric.Fields() {
				fields[k] = v
				if k == "id" {
					id := v.(float64)
					if id > sinceId {
						sinceId = id
					}
				} else if k == "id_str" {

				}
			}
			acc.AddFields(metric.Name(), fields, metric.Tags())
		}
		t.SinceID = sinceId
	}

	return nil
}

func (t *Twitter) showTimeline(screenName string, sinceId float64) ([]twitter.Tweet, error) {
	tweets, _, err := t.client.TimelineShow(screenName, sinceId)

	if err != nil {
		log.Println("showTimeline: json.Compact:", err)
		if serr, ok := err.(*json.SyntaxError); ok {
			log.Println("showTimeline: Occurred at offset:", serr.Offset)
		}
	}

	return tweets, err
}

func init() {
	inputs.Add("twitter", func() telegraf.Input {
		return &Twitter{
			client: &RealTwitterClient{},
		}
	})
}
