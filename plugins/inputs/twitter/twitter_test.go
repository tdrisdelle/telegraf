package twitter

import (
	"testing"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
)

var expectedFields = map[string]interface{}{}

func genMockTwitter() []*Twitter {
	return []*Twitter{
		&Twitter{
			client: &RealTwitterClient{},

			ScreenNames: []string{
				"biglifetechno",
			},
			ConsumerKey:    "SYyS0sNnxUH1YS3I4I1yyOXQD",
			ConsumerSecret: "KdsrRdQYz0BVzXHfY40bI4lKmTAq4vJpP66rCOVCNS0IIpACFW",
			TokenURL:       "https://api.twitter.com/oauth2/token",
		},
	}
}

func TestBasic(t *testing.T) {
	twit := genMockTwitter()

	for _, service := range twit {
		var acc testutil.Accumulator
		err := acc.GatherError(service.Gather)
		require.NoError(t, err)

		// Set responsetime
		for _, p := range acc.Metrics {
			p.Fields["response_time"] = 1.0
		}

		// tags := map[string]string{"screen_name": service.ScreenNames[0]}
		// mname := "twitter"
		// expectedFields["response_time"] = 1.0
		// acc.AssertContainsFields(t, mname, expectedFields)
	}
}
