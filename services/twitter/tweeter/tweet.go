package tweeter

import (
	"mime/multipart"
	"strconv"
	"time"
)

// Tweet represents an exisiting tweet
type Tweet struct {
	CreatedAtStr        string `json:"created_at"`
	Id                  uint64 `json:"id"`
	Text                string `json:"text"`
	User                User   `json:"user"`
	RetweetCount        uint64 `json:"retweet_count"`
	FavoriteCount       uint64 `json:"favorite_count"`
	IsFavorited         bool   `json:"favorited"`
	IsRetweeted         bool   `json:"retweeted"`
	InReplyToStatusId   uint64 `json:"in_reply_to_status_id"`
	InReplyToUserId     uint64 `json:"in_reply_to_user_id"`
	InReplyToScreenName string `json:"in_reply_to_screen_name"`
}

func (t *Tweet) CreatedAt() time.Time {
	// e.g. "Sat Aug 17 18:05:53 +0000 2019"
	tm, _ := time.Parse(time.RubyDate, t.CreatedAtStr)
	return tm
}

// StatusUpdate is used to create a new Tweet
type StatusUpdate struct {
	Status   string
	MediaIds []uint64
}

func (r *StatusUpdate) Send(c *Client) (*Tweet, error) {
	qs := "?trim_user=1" // skip user object in response

	if len(r.MediaIds) > 0 {
		qs += "&media_ids="
		for i, u := range r.MediaIds {
			if i > 0 {
				qs += ","
			}
			qs += strconv.FormatUint(u, 10)
		}
	}

	res, err := c.POSTForm("/1.1/statuses/update.json"+qs, func(m *multipart.Writer) error {
		// Note: we can't write trim_user or media_ids here for some reason
		m.WriteField("status", r.Status)
		return nil
	})
	if err != nil {
		return nil, err
	}

	tw := &Tweet{}
	return tw, res.Parse(tw)
}
