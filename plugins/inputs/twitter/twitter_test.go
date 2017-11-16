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
				"thecodeteam",
			},
			ConsumerKey:    "DXGB3b8cCeqzpiauqGwN9hgEn",
			ConsumerSecret: "nLdQsLS1FOmVOoJFZy1XSRyzqEM8osNZpIBaeTXrc0QQCEb7lk",
			TokenURL:       "https://api.twitter.com/oauth2/token",
			TagKeys: []string{
				"screen_name",
			},
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
