package twitter

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers"
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
	AccessToken    string
	AccessSecret   string
	ScreenNames    []string
	TagKeys        []string

	client TwitterClient
}

type TwitterClient interface {
	UsersShow(screenName string) (*twitter.User, *http.Response, error)
	TimelineShow(screenName string) ([]twitter.Tweet, *http.Response, error)

	SetTwitterClient(c *twitter.Client)
	TwitterClient() *twitter.Client
}

type RealTwitterClient struct {
	client *twitter.Client
}

func (c *RealTwitterClient) TimelineShow(screenName string) ([]twitter.Tweet, *http.Response, error) {
	var userTimelineParams *twitter.UserTimelineParams
	userTimelineParams = &twitter.UserTimelineParams{ScreenName: screenName, Count: 200, TrimUser: Bool(false), ExcludeReplies: Bool(true), IncludeRetweets: Bool(true)}

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

func NewHTTPClient(consumerKey string, consumerSecret string, accessToken string, accessSecret string) *http.Client {
	config := oauth1.NewConfig(consumerKey, consumerSecret)
	token := oauth1.NewToken(accessToken, accessSecret)

	return config.Client(oauth1.NoContext, token)
}

var sampleConfig = `
  ## NOTE This plugin only reads numerical measurements, strings and booleans
  ## will be ignored.
    
  consumerKey = ""
  consumerSecret = ""
  accessToken  = ""
  accessSecret = ""

  ## Twitter Token URL endpoint
  tokenURL = "https://api.twitter.com/oauth2/token"
  
  screenNames = [
  	"thecodeteam",
  ]
  
  ## List of tag names to extract from top-level of JSON server response
  tag_keys = [
    "id_str",
	"retweeted",
	"favorited",
  ]
  
  fieldpass = ["user_statuses_count", "user_favourites_count", "user_followers_count", "user_friends_count", "user_listed_count", "retweet_count", "favorite_count", "created_at", "text"]
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
		httpClient := NewHTTPClient(t.ConsumerKey, t.ConsumerSecret, t.AccessToken, t.AccessSecret)
		client := twitter.NewClient(httpClient)
		t.client.SetTwitterClient(client)
	}

	for _, screenname := range t.ScreenNames {
		wg.Add(1)
		go func(screenname string) {
			defer wg.Done()
			acc.AddError(t.gatherTimeline(acc, screenname))
		}(screenname)
	}

	wg.Wait()

	return nil
}

func (t *Twitter) gatherTimeline(
	acc telegraf.Accumulator,
	screenName string,
) error {
	tweets, err := t.showTimeline(screenName)
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

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, t.TagKeys, tags)
	if err != nil {
		return err
	}
	tweetsBytes, err := json.Marshal(tweets)
	if err != nil {
		return err
	}

	metrics, err := parser.Parse(tweetsBytes)
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

func (t *Twitter) showTimeline(screenName string) ([]twitter.Tweet, error) {
	tweets, _, err := t.client.TimelineShow(screenName)

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
