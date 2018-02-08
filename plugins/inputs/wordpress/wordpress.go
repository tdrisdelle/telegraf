package wordpress

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
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
	PostsURI            string
	TagStatsURI         string
	TopPostsTagKeys     []string
	SummaryStatsTagKeys []string
	PostsTagKeys        []string
	TagStatsTagKeys     []string
	Method              string
	ResponseTimeout     internal.Duration
	Parameters          map[string]string
	Headers             map[string]string

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

  topPostsStatsURI = "https://public-api.wordpress.com/rest/v1.1/sites/YOUR_SITE_ID/stats/top-posts?fields=days&max=100"
  summaryStatsURI = "https://public-api.wordpress.com/rest/v1.1/sites/YOUR_SITE_ID/stats/summary"
  postsURI = "https://public-api.wordpress.com/rest/v1.1/sites/YOUR_SITE_ID/posts?fields=ID,author,date,modified,title,URL,tags,categories"
  tagStatsURI = "https://public-api.wordpress.com/rest/v1.1/sites/YOUR_SITE_ID/stats/tags"
  
  ## Set response_timeout (default 5 seconds)
  response_timeout = "5s"

  ## List of tag names to extract from top-level of JSON server response
  top_posts_tag_keys = ["id",]
	
  summary_stats_tag_keys = []
	
  posts_tag_keys = ["ID",]
  
  tag_stats_tag_keys = []
  
  ## HTTP Headers (all values must be strings)
  [inputs.wordpress.headers]
     authorization = "Bearer YOUR_BEARER_TOKEN"
  
`

func (w *Wordpress) SampleConfig() string {
	return sampleConfig
}

func (w *Wordpress) Description() string {
	return "Read flattened metrics from Wordpress HTTP endpoints"
}

// Gathers data for blog posts.
func (w *Wordpress) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if w.client.HTTPClient() == nil {
		tr := &http.Transport{
			ResponseHeaderTimeout: w.ResponseTimeout.Duration,
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
		if w.TopPostsStatsURI != "" {
			acc.AddError(w.gatherTopPostsStats(acc))
		}
		if w.SummaryStatsURI != "" {
			acc.AddError(w.gatherSummaryStats(acc))
		}
		if w.PostsURI != "" {
			acc.AddError(w.gatherPosts(acc))
		}
		if w.TagStatsURI != "" {
			acc.AddError(w.gatherTagStats(acc))
		}
	}()

	wg.Wait()

	return nil
}

/*
Gathers data from a wordpress stats endpoint about top posts. JSON return format is an array with
a nesting that must be transformed from this:
{
    "days": {
        "2018-02-05": {
            "postviews": [
                {
                    "id": 8516,
                    "href": "https:\/\/blog.wordpress.com\/2018\/02\/01\/foo\/",
                    "date": "2018-02-01 09:15:27",
                    "title": "Foo",
                    "type": "post",
                    "views": 16,
                    "video_play": false
                },
                {
                    "id": 6987,
                    "href": "https:\/\/blog.wordpress.com\/2017\/06\/05\/bar\/",
                    "date": "2017-06-05 16:17:01",
                    "title": "Bar",
                    "type": "post",
                    "views": 16,
                    "video_play": false
                },
            ],
            "total_views": "126",
            "other_views": 41
        }
    }
}

...into a higher-level array so that each tag becomes its own measurement. To do this, the JSON must
end up looking like this:
[
    {
        "id": 8516,
        "href": "https:\/\/blog.wordpress.com\/2018\/02\/01\/foo\/",
        "date": "2018-02-01 09:15:27",
        "title": "Foo",
        "type": "post",
        "views": 16,
        "video_play": false
    },
    {
        "id": 6987,
        "href": "https:\/\/blog.wordpress.com\/2017\/06\/05\/bar\/",
        "date": "2017-06-05 16:17:01",
        "title": "Bar",
        "type": "post",
        "views": 16,
        "video_play": false
    }
]

...and will result in measurements that look like this:
wordpress_topposts,id=8516 views=16,video_play=false,href="https://blog.wordpress.com/2018/02/01/foo/",date="2018-02-01 09:15:27",title="Foo",type="post" 1517849912000000000
wordpress_topposts,id=6987 date="2017-06-05 16:17:01",title="Bar",type="post",views=16,video_play=false,href="https://blog.wordpress.com/2017/06/05/bar/" 1517849912000000000

	Parameters:
		acc      : The telegraf Accumulator to use

	Returns:
		error: Any error that may have occurred
*/
func (w *Wordpress) gatherTopPostsStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.TopPostsStatsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "wordpress_topposts"
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, w.TopPostsTagKeys, tags)
	if err != nil {
		return err
	}

	reStr := regexp.MustCompile("^(.*?)(\\[.*\\])(,\"total_views\":.*)$")
	repStr := "$2"
	resp = reStr.ReplaceAllString(resp, repStr)

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

/*
Gathers data from a wordpress posts endpoint about posts. JSON return format is an array with
a nesting that must be transformed from this:
{
	"found": 366,
	"posts": [
		{
			"ID": 8552,
			"author": {
				"ID": 425,
				"login": "authorAlpha@gmail.com",
				"email": false,
				"name": "[email protected]",
				"first_name": "",
				"last_name": "",
				"nice_name": "community",
				"URL": "",
				"avatar_URL": "https://secure.gravatar.com/avatar/xxxxxxxxxxx?s=96&d=mm&r=g",
				"profile_URL": "http://en.gravatar.com/xxxxxxxxxxx"
			},
			"date": "2018-02-05T09:26:45-05:00",
			"modified": "2018-02-05T13:07:01-05:00",
			"title": "Foo",
			"URL": "https://blog.wordpress.com/2018/02/05/foo/",
			"tags": {
				"Tag A": {
					"ID": 111,
					"name": "Tag A",
					"slug": "tag-a",
					"description": "",
					"post_count": 78,
					"meta": {
						"links": {
							"self": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-a",
							"help": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-a/help",
							"site": "https://public-api.wordpress.com/rest/v1.1/sites/1234567"
						}
					}
				}
			},
			"categories": {
				"Category A": {
					"ID": 257,
					"name": "Category A",
					"slug": "category-a",
					"description": "",
					"post_count": 4,
					"meta": {
						"links": {
							"self": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/categories/slug:category-a",
							"help": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/categories/slug:category-a/help",
							"site": "https://public-api.wordpress.com/rest/v1.1/sites/1234567"
						}
					},
					"parent": 0
				}
			}
		},
		{
			"ID": 8535,
			"author": {
				"ID": 5,
				"login": "authorBravo",
				"email": false,
				"name": "Author Bravo",
				"first_name": "Author",
				"last_name": "Bravo",
				"nice_name": "authorbravo",
				"URL": "",
				"avatar_URL": "https://secure.gravatar.com/avatar/yyyyyyyyyyyyy?s=96&d=mm&r=g",
				"profile_URL": "http://en.gravatar.com/yyyyyyyyyyyyy"
			},
			"date": "2018-02-05T09:05:59-05:00",
			"modified": "2018-02-05T10:23:05-05:00",
			"title": "Bar",
			"URL": "https://blog.wordpress.com/2018/02/05/bar/",
			"tags": {
				"Tag A": {
					"ID": 111,
					"name": "Tag A",
					"slug": "tag-a",
					"description": "",
					"post_count": 78,
					"meta": {
						"links": {
							"self": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-a",
							"help": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-a/help",
							"site": "https://public-api.wordpress.com/rest/v1.1/sites/1234567"
						}
					}
				},
				"Tag B": {
					"ID": 222,
					"name": "Tag B",
					"slug": "tag-b",
					"description": "",
					"post_count": 13,
					"meta": {
						"links": {
							"self": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-b",
							"help": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/tags/slug:tag-b/help",
							"site": "https://public-api.wordpress.com/rest/v1.1/sites/1234567"
						}
					}
				}
			},
			"categories": {
				"Category B": {
					"ID": 149,
					"name": "Category B",
					"slug": "category-b",
					"description": "",
					"post_count": 110,
					"meta": {
						"links": {
							"self": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/categories/slug:category-b",
							"help": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/categories/slug:category-b/help",
							"site": "https://public-api.wordpress.com/rest/v1.1/sites/1234567"
						}
					},
					"parent": 0
				}
			}
		}
	],
	"meta": {
		"links": {
			"counts": "https://public-api.wordpress.com/rest/v1.1/sites/1234567/post-counts/post"
		},
		"next_page": "value=2018-02-05T09%3A05%3A59-05%3A00&id=8535"
	}
}

...into a higher-level array so that each tag becomes its own measurement. To do this, the JSON must
end up looking like this:
[
	{
		"ID": 8552,
		"author": {
			"ID": 425,
			"login": "authorAlpha@gmail.com",
			"email": false,
			"name": "[email protected]",
			"first_name": "",
			"last_name": "",
			"nice_name": "community",
			"URL": "",
			"avatar_URL": "https://secure.gravatar.com/avatar/xxxxxxxxxxx?s=96&d=mm&r=g",
			"profile_URL": "http://en.gravatar.com/xxxxxxxxxxx"
		},
		"date": "2018-02-05T09:26:45-05:00",
		"modified": "2018-02-05T13:07:01-05:00",
		"title": "Foo",
		"URL": "https://blog.wordpress.com/2018/02/05/foo/",
		"tags": {
			"Tag A": {
				"ID": 111,
				"name": "Tag A",
				"slug": "tag-a",
				"description": "",
				"post_count": 78,
			}
		},
		"categories": {
			"Category A": {
				"ID": 257,
				"name": "Category A",
				"slug": "category-a",
				"description": "",
				"post_count": 4,
				"parent": 0
			}
		}
	},
	{
		"ID": 8535,
		"author": {
			"ID": 5,
			"login": "authorBravo",
			"email": false,
			"name": "Author Bravo",
			"first_name": "Author",
			"last_name": "Bravo",
			"nice_name": "authorbravo",
			"URL": "",
			"avatar_URL": "https://secure.gravatar.com/avatar/yyyyyyyyyyyyy?s=96&d=mm&r=g",
			"profile_URL": "http://en.gravatar.com/yyyyyyyyyyyyy"
		},
		"date": "2018-02-05T09:05:59-05:00",
		"modified": "2018-02-05T10:23:05-05:00",
		"title": "Bar",
		"URL": "https://blog.wordpress.com/2018/02/05/bar/",
		"tags": {
			"Tag A": {
				"ID": 111,
				"name": "Tag A",
				"slug": "tag-a",
				"description": "",
				"post_count": 78,
			},
			"Tag B": {
				"ID": 222,
				"name": "Tag B",
				"slug": "tag-b",
				"description": "",
				"post_count": 13,
			}
		},
		"categories": {
			"Category B": {
				"ID": 149,
				"name": "Category B",
				"slug": "category-b",
				"description": "",
				"post_count": 110,
				"parent": 0
			}
		}
	}
]


...and will result in measurements that look like this:
wordpress_posts,ID=8552 author_URL="",author_profile_URL="http://en.gravatar.com/x",author_login="author0@gmail.com",date="2018-02-05T09:26:45-05:00",author_last_name="",author_name="author0@gmail.com",author_ID=425,author_first_name="",author_email=false,modified="2018-02-05T09:26:45-05:00",URL="https://blog.wordpress.com/2018/02/05/foo/",tags="tag_a",author_nice_name="author0gmail-com",title="Foo",author_avatar_URL="https://secure.gravatar.com/avatar/x?s=96&d=mm&r=g",categories="cat_a" 1517851161000000000
wordpress_posts,ID=8535 author_profile_URL="http://en.gravatar.com/y",author_ID=5,author_last_name="2",author_login="author2",author_URL="",author_name="Author 2",author_avatar_URL="https://secure.gravatar.com/avatar/y?s=96&d=mm&r=g",modified="2018-02-05T10:23:05-05:00",author_nice_name="author2",tags="tag_a,tag_b",author_email=false,title="Bar",categories="cat_b",author_first_name="Author",date="2018-02-05T09:05:59-05:00",URL="https://blog.wordpress.com/2018/02/05/bar/" 1517851161000000000

	Parameters:
		acc      : The telegraf Accumulator to use

	Returns:
		error: Any error that may have occurred
*/
func (w *Wordpress) gatherPosts(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.PostsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "wordpress_posts"
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, w.PostsTagKeys, tags)
	if err != nil {
		return err
	}

	// Strip out all of the "meta" blocks
	reStr := regexp.MustCompile(",\"meta\":\\s*{.*?}}")
	repStr := ""
	resp = reStr.ReplaceAllString(resp, repStr)

	// Trim off the leading preamble
	reStr = regexp.MustCompile("^.*?(\\[.*\\])")
	repStr = "$1"
	resp = reStr.ReplaceAllString(resp, repStr)

	metrics, err := parser.Parse([]byte(resp))
	if err != nil {
		return err
	}

	var categories_val string
	var tags_val string
	for _, metric := range metrics {
		fields := make(map[string]interface{})
		for k, v := range metric.Fields() {
			if strings.HasPrefix(k, "categories") && strings.HasSuffix(k, "slug") {
				categories_val = v.(string) + "," + categories_val
			} else if strings.HasPrefix(k, "tags") && strings.HasSuffix(k, "slug") {
				tags_val = v.(string) + "," + tags_val
			} else if !strings.HasPrefix(k, "categories") && !strings.HasPrefix(k, "tags") {
				fields[k] = v
			}
		}
		if len(categories_val) > 0 {
			fields["categories"] = strings.TrimSuffix(categories_val, ",")
		}
		if len(tags_val) > 0 {
			fields["tags"] = strings.TrimSuffix(tags_val, ",")
		}
		acc.AddFields(metric.Name(), fields, metric.Tags())
		categories_val = ""
		tags_val = ""
	}

	return nil
}

/*
Gathers data from a wordpress stats endpoint about site summary. JSON return format is an array with
a nesting that looks like this:
	{
	    "date": "2018-02-05",
	    "period": "day",
	    "views": 127,
	    "visitors": 97,
	    "likes": 0,
	    "reblogs": 0,
	    "comments": 0,
	    "followers": 3
	}

	Parameters:
		acc      : The telegraf Accumulator to use

	Returns:
		error: Any error that may have occurred
*/
func (w *Wordpress) gatherSummaryStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.SummaryStatsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "wordpress_summary"
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
		acc.AddFields(metric.Name(), fields, metric.Tags())
	}

	return nil
}

/*
Gather data from a wordpress stats endpoint about tags & categories. JSON return format is an array with
a nesting that must be transformed from this:

 {
    "date": "2018-02-05",
    "tags": [
        {
            "tags": [
                {
                    "type": "category",
                    "name": "Technical",
                    "link": "http:\/\/blog.wordpress.com\/?taxonomy=category&#038;term=technical"
                }
            ],
            "views": 902
        },
        {
            "tags": [
                {
                    "type": "tag",
                    "name": "Kubernetes",
                    "link": "http:\/\/blog.wordpress.com\/?taxonomy=post_tag&#038;term=kubernetes"
                }
            ],
            "views": 774
        },
}

...into a higher-level array so that each tag becomes its own measurement. To do this, the JSON must
end up looking like this:
[
    {
		"type": "category",
        "name": "Technical",
        "link": "http:\/\/blog.wordpress.com\/?taxonomy=category&#038;term=technical",
        "views": 902
    },
    {
        "type": "tag",
        "name": "Kubernetes",
        "link": "http:\/\/blog.wordpress.com\/?taxonomy=post_tag&#038;term=kubernetes",
        "views": 774
    }
]

...and will result in measurements that look like this:
wordpress_tagstats,type=category,name=Technical link="http:\/\/blog.wordpress.com\/?taxonomy=category&#038;term=technical",views=902
wordpress_tagstats,type=tag,name=Kubernetes link="http:\/\/blog.wordpress.com\/?taxonomy=tag&#038;term=kubernetes",views=774

	Parameters:
    	acc     : The telegraf Accumulator to use

	Returns:
    	error	: Any error that may have occurred
*/
func (w *Wordpress) gatherTagStats(
	acc telegraf.Accumulator,
) error {
	resp, _, err := w.sendRequest(w.TagStatsURI)
	if err != nil {
		return err
	}

	msrmnt_name := "wordpress_tagstats"
	tags := map[string]string{}

	parser, err := parsers.NewJSONLiteParser(msrmnt_name, w.TagStatsTagKeys, tags)
	if err != nil {
		return err
	}

	// strip everything through the "date" field and the top-level "tags" field,
	// leaving only the surrounding brackets [].
	// also strip the trailing brace }
	reStr := regexp.MustCompile("^[^\\[]+(.*\\])\\}")
	repStr := "$1"
	resp = reStr.ReplaceAllString(resp, repStr)

	metrics, err := parser.Parse([]byte(resp))
	if err != nil {
		return err
	}

	for _, metric := range metrics {
		fields := make(map[string]interface{})
		for k, v := range metric.Fields() {
			k = strings.Replace(k, "tags_0_", "", 1)
			if k == "type" || k == "name" {
				metric.AddTag(k, v.(string))
			} else {
				fields[k] = v
			}
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
