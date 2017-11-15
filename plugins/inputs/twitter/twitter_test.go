package twitter

import (
	"fmt"
	"testing"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
)

var expectedFields = map[string]interface{}{
	"parent_child": float64(3),
}

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
		},
	}
}

func TestBasic(t *testing.T) {
	twit := genMockTwitter()

	fmt.Printf("doing stuff\n")
	for _, service := range twit {
		var acc testutil.Accumulator
		err := acc.GatherError(service.Gather)
		require.NoError(t, err)

		for _, sn := range service.ScreenNames {
			fmt.Printf("screenName: %+v\n", sn)
			tags := map[string]string{"screen_name": sn}
			mname := "twitter_" + service.Name
			expectedFields["response_time"] = 1.0
			acc.AssertContainsTaggedFields(t, mname, expectedFields, tags)
		}
	}
	fmt.Printf("done\n")
}
