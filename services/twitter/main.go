package main

import (
	// "fmt"
	// "os"
	// "time"
	// "bytes"
	// "encoding/json"
	// "context"

	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rsms/go-log"
	memex "github.com/rsms/memex/service-api"
	"github.com/rsms/memex/services/twitter/tweeter"
)

var DATA_DIR string = "./data"
var DEBUG = false
var NEW_FILE_MODE fs.FileMode = 0600 // permissions for new files
var TWEETS_DIR = "tweets"            // sibdir of DATA_DIR where tweet json files are stored

type IdRange struct {
	min, max uint64
}

func onMemexMsg(cmd string, args [][]byte) ([][]byte, error) {
	log.Info("onMemexMsg %q %v", cmd, args)
	return nil, nil
}

func main() {
	defer log.Sync()

	// DATA_DIR
	if os.Getenv("MEMEX_DIR") != "" {
		DATA_DIR = filepath.Join(os.Getenv("MEMEX_DIR"), "twitter")
	} else {
		var err error
		DATA_DIR, err = filepath.Abs(DATA_DIR)
		if err != nil {
			panic(err)
		}
	}

	// parse CLI flags
	optDebug := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	// update log level based on CLI options
	log.RootLogger.EnableFeatures(log.FMilliseconds)
	if *optDebug {
		DEBUG = true
		log.RootLogger.Level = log.LevelDebug
		log.RootLogger.EnableFeatures(log.FSync)
	} else {
		log.RootLogger.EnableFeatures(log.FSyncError)
	}

	// connect to memex
	if err := memex.Connect(onMemexMsg); err != nil && err != memex.ErrNoMemex {
		log.Warn("failed to commect to memex service: %v", err)
	}

	// test memex IPC
	if memex.Connected {
		// disable log prefix
		log.RootLogger.DisableFeatures(log.FTime | log.FMilliseconds |
			log.FPrefixDebug | log.FPrefixInfo | log.FPrefixWarn | log.FPrefixError)

		log.Info("connected to memex service; sending \"ping\" message")
		res, err := memex.Command("ping", []byte("hello"))
		if err != nil {
			panic(err)
		}
		log.Info("\"ping\" result from memex service: %q", res)
	}

	log.Info("DATA_DIR=%q", DATA_DIR)

	// configure twitter client
	tw, err := createTwitterClient("./twitter-credentials.ini")
	if err != nil {
		panic(err)
	}

	downloadTweets(tw)
}

func getStoredTweetIdRange() (min_id uint64, max_id uint64) {
	min_id = 0
	max_id = 0
	tweetsDir := datapath(TWEETS_DIR)
	f, err := os.Open(tweetsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error("failed to open directory %q: %v", tweetsDir, err)
		}
		return
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		log.Error("failed to read directory %q: %v", tweetsDir, err)
	}
	if len(names) > 0 {
		sort.Strings(names)
		// min_id
		for _, name := range names {
			if len(name) > 0 && name[0] != '.' && strings.HasSuffix(name, ".json") {
				// oldest tweet probably found. parse as uint (sans ".json")
				id, err := strconv.ParseUint(name[:len(name)-len(".json")], 10, 64)
				if err == nil {
					min_id = id
					break
				}
			}
		}
		// max_id
		for i := len(names) - 1; i >= 0; i-- {
			name := names[i]
			if len(name) > 0 && name[0] != '.' && strings.HasSuffix(name, ".json") {
				// most recent tweet probably found. parse as uint (sans ".json")
				id, err := strconv.ParseUint(name[:len(name)-len(".json")], 10, 64)
				if err == nil {
					max_id = id
					break
				}
			}
		}
	}
	return
}

// downloadTweets fetches old tweets.
// It is limited to ~3000 tweets into the past (as of May 2021.)
// See https://twitter.com/settings/download_your_data for an alterante way to get old tweets.
func downloadTweets(tw *tweeter.Client) {
	// find oldest tweet we have already saved
	minStoredId, maxStoredId := getStoredTweetIdRange()
	log.Info("minStoredId, maxStoredId: %v, %v", minStoredId, maxStoredId)

	var max_id, since_id uint64
	isFetchingHistory := false

	// if we have existing tweets, we start by fetching additional old tweets
	if minStoredId != 0 {
		isFetchingHistory = true
		max_id = minStoredId
	}

	const sleeptimeMax time.Duration = 10 * time.Second
	var sleeptime time.Duration
	bumpSleeptime := func(initial time.Duration) {
		if sleeptime == 0 {
			sleeptime = initial
		} else {
			sleeptime *= 2
			if sleeptime > sleeptimeMax {
				sleeptime = sleeptimeMax
			}
		}
	}

	for {
		// fetch from API
		idrange, err := downloadSomeTweets(tw, max_id, since_id)
		if err != nil {
			// error
			if rl, ok := err.(tweeter.RateLimitError); ok {
				// rate-limited
				log.Warn("rate limited: %v/%v req remaining, reset at %v",
					rl.RateLimitRemaining(), rl.RateLimit(), rl.RateLimitReset())
				if rl.RateLimitRemaining() == 0 {
					sleeptime = time.Until(rl.RateLimitReset().Add(100 * time.Millisecond))
				} else {
					bumpSleeptime(500 * time.Millisecond)
				}
			} else {
				log.Error("error while downloading tweets: %v", err)
				// if e, ok := err.(tweeter.ResponseError); ok {
				// 	// communication error (e.g. network error, server error, etc)
				// }
				bumpSleeptime(100 * time.Millisecond)
			}
			if sleeptime > 0 {
				// wait a bit before we retry
				if sleeptime > sleeptimeMax {
					log.Info("retrying at %v...", time.Now().Add(sleeptime))
				} else {
					log.Info("retrying in %v...", sleeptime)
				}
				time.Sleep(sleeptime)
			}
		} else {
			// success
			sleeptime = 0 // reset sleeptime
			maxStoredId = u64max(maxStoredId, idrange.max)
			if idrange.min != 0 {
				minStoredId = u64min(minStoredId, idrange.min)
			}

			if idrange.min == max_id || idrange.min == 0 {
				// empty; no tweets found
				if isFetchingHistory {
					log.Info("no more tweets returned by the Twitter API for max_id=%v", max_id)
					isFetchingHistory = false
				} else {
					// As we are polling for new tweets; wait a little before retrying.
					// API rate limit of 900/15min means our upper frequency limit is 1req/sec
					waittime := 5 * time.Second
					log.Info("checking again in %v...", waittime)
					time.Sleep(waittime)
				}
			} else {
				// did find some tweets
				log.Info("downloaded tweets in id range %v-%v", idrange.min, idrange.max)
			}

			if isFetchingHistory {
				// fetching old tweets. Moves: past <- now
				max_id = minStoredId - 1 // API max_id has semantics "LESS OR EQUAL" (<=)
				since_id = 0
			} else {
				// fetching new tweets. Moves: past -> now
				max_id = 0
				since_id = maxStoredId // API since_id has semantics "GREATER" (>)
			}
		}
	}
}

func mockRateLimitError(limit uint32, remaining uint32, reset time.Time) tweeter.RateLimitError {
	return tweeter.RateLimitError{
		Limit:     limit,
		Remaining: remaining,
		Reset:     reset,
	}
}

func mockResponseError(code int, body string) tweeter.ResponseError {
	return tweeter.NewResponseError(code, body)
}

// downloadSomeTweets downloads tweets which are older than max_id
func downloadSomeTweets(tw *tweeter.Client, max_id, since_id uint64) (IdRange, error) {
	// Note that max_id=0 makes twFetchUserTimeline omit the id and fetch the most recent tweets.

	if max_id == 0 && since_id == 0 {
		log.Info("fetching recent tweets")
	} else if max_id == 0 {
		log.Info("checking for tweets newer than id %v", since_id)
	} else {
		log.Info("fetching tweets older than id %v", max_id+1 /* "<=" -> "<" */)
	}

	// mock error responses for testing & debugging
	// return mockRateLimitError(900, 0, time.Now().Add(3*time.Second)), 0
	// return mockRateLimitError(900, 10, time.Now().Add(3*time.Second)), 0

	var idrange IdRange

	// talk with the twitter API (upper limit on "count" is 200 as of May 2021)
	res, err := twFetchUserTimeline(tw, 200, max_id, since_id)
	if err != nil {
		// an error here is a network error.
		// res.Parse is where we get API errors.
		return idrange, err
	}
	// json_prettyprint(res.ReadBody())

	// log rate limits
	log.Info("twitter API rate limit: %v/%v req remaining, reset at %v",
		res.RateLimitRemaining(), res.RateLimit(), res.RateLimitReset())

	// parse the API response
	var tweets []map[string]interface{} // [{key:value}...]
	if err := res.Parse(&tweets); err != nil {
		return idrange, err
	}

	// parse the tweets json
	for _, tweet := range tweets {
		// extract id_str property
		id_str, ok := tweet["id_str"].(string)
		if !ok {
			err = fmt.Errorf("unexpected json from API response: id_str is not a string\n%+v", tweet)
			return idrange, err
		}

		// parse id_str property as uint
		var id uint64
		if id, err = strconv.ParseUint(id_str, 10, 64); err != nil {
			err = fmt.Errorf("failed to parse id_str %q: %v", id_str, err)
			return idrange, err
		}

		idrange.max = u64max(idrange.max, id)
		// idrange.min = u64min(idrange.min, id)
		if id < idrange.min || idrange.min == 0 {
			idrange.min = id
		}

		// // print
		// jsondata, _ := json.MarshalIndent(tweet, "", "  ")
		// log.Info("tweet %#v: %s", id, jsondata)

		// write json file
		jsonfile := datapath(TWEETS_DIR, id_str+".json")
		var f io.WriteCloser
		if f, err = createFileAtomic(jsonfile, NEW_FILE_MODE); err != nil {
			return idrange, err
		}
		jsonenc := json.NewEncoder(f)
		jsonenc.SetIndent("", "  ")
		err = jsonenc.Encode(tweet)
		f.Close()
		if err != nil {
			return idrange, fmt.Errorf("failed to encode json %q: %v", jsonfile, err)
		}
		log.Info("wrote %s", nicepath(jsonfile))
	}

	// TODO: when we encounter a tweet with...
	// - "in_reply_to_status_id": 1396149090375720960 -- fetch that tweet
	// - "in_reply_to_user_id": 14199907 -- fetch that user
	return idrange, nil
}

type writeFileTransaction struct {
	f        *os.File
	filename string
	mode     fs.FileMode
}

func (t *writeFileTransaction) Write(p []byte) (n int, err error) {
	return t.f.Write(p)
}

func (t *writeFileTransaction) Close() error {
	if err := t.f.Close(); err != nil {
		return err
	}
	return renameFile(t.f.Name(), t.filename, t.mode)
}

func createFileAtomic(filename string, mode fs.FileMode) (io.WriteCloser, error) {
	f, err := os.CreateTemp("", "memex")
	return &writeFileTransaction{f, filename, mode}, err
}

func twFetchUserTimeline(
	tw *tweeter.Client, count, max_id, since_id uint64) (*tweeter.ApiResponse, error) {
	// https://developer.twitter.com/en/docs/twitter-api/v1/tweets/timelines/api-reference/get-statuses-user_timeline
	path := "/1.1/statuses/user_timeline.json?trim_user=true&include_rts=false&tweet_mode=extended"
	if count > 0 {
		path += fmt.Sprintf("&count=%v", count)
	}
	if max_id > 0 {
		path += fmt.Sprintf("&max_id=%v", max_id)
	}
	if since_id > 0 {
		path += fmt.Sprintf("&since_id=%v", since_id)
	}
	//log.Debug("path %q", path)
	res, err := tw.GET(path, nil)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func createTwitterClient(credentialsFile string) (*tweeter.Client, error) {
	creds, err := loadINIFile(credentialsFile)
	if err != nil {
		return nil, err
	}
	getcred := func(k string) string {
		v := creds[k]
		if v == "" {
			err = fmt.Errorf("missing credentials key %q", k)
		}
		return v
	}
	params := &tweeter.ClientParams{
		ConsumerKey:       getcred("ConsumerKey"),
		ConsumerSecret:    getcred("ConsumerSecret"),
		AccessToken:       getcred("AccessToken"),
		AccessTokenSecret: getcred("AccessTokenSecret"),
	}
	return tweeter.NewClient(params), err
}

// loadINIFile reads an INI-style file, with lines of the format "key:value" or "key=value".
// Skips empty lines and lines starting with ";" or "#". Does not support sections ("[section]".)
func loadINIFile(filename string) (map[string]string, error) {
	buf, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for lineno, line := range bytes.Split(buf, []byte("\n")) {
		if len(line) == 0 || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			return m, fmt.Errorf("sections are no supported (%s:%d)", filename, lineno+1)
		}
		i := bytes.IndexByte(line, ':')
		if i == -1 {
			i = bytes.IndexByte(line, '=')
			if i == -1 {
				return m, fmt.Errorf("key without value (%s:%d)", filename, lineno+1)
			}
		}
		key := string(line[:i])
		value := string(bytes.TrimLeft(line[i+1:], " \t"))
		m[key] = value
	}
	return m, nil
}

func json_prettyprint(buf []byte) {
	var buf2 bytes.Buffer
	json.Indent(&buf2, buf, "", "  ")
	os.Stdout.Write(buf2.Bytes())
	os.Stdout.Write([]byte("\n"))
}

func createFile(filename string) (*os.File, error) {
	return os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, NEW_FILE_MODE)
}

func datapath(names ...string) string {
	names = append([]string{DATA_DIR}, names...)
	return filepath.Join(names...)
}

func nicepath(path string) string {
	if s, err := filepath.Rel(DATA_DIR, path); err == nil {
		if !strings.HasPrefix(s, "../") {
			return s
		}
	}
	return path
}

func u64min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func u64max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
