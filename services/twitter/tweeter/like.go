package tweeter

// Like is "favorite", recorded when you tap a heart in Twitter
type Like struct {
	CreatedAtStr   string `json:"created_at"`
	Id             uint64 `json:"id"`
	User           User   `json:"user"`
	QuotedStatusId uint64 `json:"quoted_status_id"`
	QuotedStatus   *Tweet `json:"quoted_status"`
	IsTruncated    bool   `json:"truncated"`
}
