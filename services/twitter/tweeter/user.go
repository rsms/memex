package tweeter

type User struct {
	Id             uint64 `json:"id"`
	Name           string `json:"name"`
	ScreenName     string `json:"screen_name"`
	Location       string `json:"location"`
	Description    string `json:"description"`
	Url            string `json:"url"`
	CreatedAtStr   string `json:"created_at"`
	FollowersCount uint64 `json:"followers_count"`
	FollowingCount uint64 `json:"friends_count"`
	FavoritesCount uint64 `json:"favourites_count"`
}
