// This code is based on https://github.com/kurrik/twittergo licensed as follows (Apache 2.0)
// Copyright 2019 Arne Roomann-Kurrik
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
package tweeter

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/kurrik/oauth1a"
)

const (
	MaxTweetLength = 280
	UrlTweetLength = 23 // number of characters a URL debit from MaxTweetLength
)

type Client struct {
	Host       string
	OAuth      *oauth1a.Service
	User       *oauth1a.UserConfig
	HttpClient *http.Client
	AppToken   string
}

type ClientParams struct {
	ConsumerKey       string
	ConsumerSecret    string
	AccessToken       string
	AccessTokenSecret string
}

func NewClient(p *ClientParams) *Client {
	config := &oauth1a.ClientConfig{
		ConsumerKey:    p.ConsumerKey,
		ConsumerSecret: p.ConsumerSecret,
	}
	user := oauth1a.NewAuthorizedConfig(p.AccessToken, p.AccessTokenSecret)
	return NewClientWithConfig(config, user)
}

func NewClientWithConfig(config *oauth1a.ClientConfig, user *oauth1a.UserConfig) *Client {
	var (
		host      = "api.twitter.com"
		base      = "https://" + host
		req, _    = http.NewRequest("GET", "https://api.twitter.com", nil)
		proxy, _  = http.ProxyFromEnvironment(req)
		transport *http.Transport
		tlsconfig *tls.Config
	)
	if proxy != nil {
		tlsconfig = &tls.Config{
			InsecureSkipVerify: getEnvEitherCase("TLS_INSECURE") != "",
		}
		if tlsconfig.InsecureSkipVerify {
			log.Println("WARNING: SSL cert verification disabled")
		}
		transport = &http.Transport{
			Proxy:           http.ProxyURL(proxy),
			TLSClientConfig: tlsconfig,
		}
	} else {
		transport = &http.Transport{}
	}
	return &Client{
		Host: host,
		HttpClient: &http.Client{
			Transport: transport,
		},
		User: user,
		OAuth: &oauth1a.Service{
			RequestURL:   base + "/oauth/request_token",
			AuthorizeURL: base + "/oauth/authorize",
			AccessURL:    base + "/oauth/access_token",
			ClientConfig: config,
			Signer:       new(oauth1a.HmacSha1Signer),
		},
	}
}

// Sends a HTTP request through this instance's HTTP client.
func (c *Client) HttpRequest(req *http.Request) (resp *ApiResponse, err error) {
	if len(req.URL.Scheme) == 0 {
		req.URL, err = url.Parse("https://" + c.Host + req.URL.String())
		if err != nil {
			return
		}
	}
	if c.User != nil {
		c.OAuth.Sign(req, c.User)
	} else if err = c.Sign(req); err != nil {
		return
	}
	var r *http.Response
	r, err = c.HttpClient.Do(req)
	resp = (*ApiResponse)(r)
	return
}

func (c *Client) GET(endpoint string, headers map[string]string) (*ApiResponse, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	return c.HttpRequest(req)
}

func (c *Client) POSTForm(endpoint string, fw func(*multipart.Writer) error) (*ApiResponse, error) {
	body := bytes.NewBufferString("")
	mp := multipart.NewWriter(body)
	fw(mp)
	mp.Close()
	req, err := http.NewRequest("POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "multipart/form-data;boundary="+mp.Boundary())
	req.Header.Set("Content-Length", strconv.Itoa(body.Len()))
	return c.HttpRequest(req)
}

// Changes the user authorization credentials for this client.
func (c *Client) SetUser(user *oauth1a.UserConfig) {
	c.User = user
}

func (c *Client) FetchAppToken() (err error) {
	var (
		req  *http.Request
		resp *http.Response
		rb   []byte
		rj   = map[string]interface{}{}
		url  = fmt.Sprintf("https://%v/oauth2/token", c.Host)
		ct   = "application/x-www-form-urlencoded;charset=UTF-8"
		body = "grant_type=client_credentials"
		ek   = oauth1a.Rfc3986Escape(c.OAuth.ClientConfig.ConsumerKey)
		es   = oauth1a.Rfc3986Escape(c.OAuth.ClientConfig.ConsumerSecret)
		cred = fmt.Sprintf("%v:%v", ek, es)
		ec   = base64.StdEncoding.EncodeToString([]byte(cred))
		h    = fmt.Sprintf("Basic %v", ec)
	)
	req, err = http.NewRequest("POST", url, bytes.NewBufferString(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", h)
	req.Header.Set("Content-Type", ct)
	if resp, err = c.HttpClient.Do(req); err != nil {
		return
	}
	if resp.StatusCode != 200 {
		err = fmt.Errorf("Got HTTP %v instead of 200", resp.StatusCode)
		return
	}
	if rb, err = ioutil.ReadAll(resp.Body); err != nil {
		return
	}
	if err = json.Unmarshal(rb, &rj); err != nil {
		return
	}
	var (
		token_type   = rj["token_type"].(string)
		access_token = rj["access_token"].(string)
	)
	if token_type != "bearer" {
		err = fmt.Errorf("Got invalid token type: %v", token_type)
	}
	c.AppToken = access_token
	return nil
}

// Signs the request with app-only auth, fetching a bearer token if needed.
func (c *Client) Sign(req *http.Request) error {
	if len(c.AppToken) == 0 {
		if err := c.FetchAppToken(); err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.AppToken)
	return nil
}

func getEnvEitherCase(k string) string {
	if v := os.Getenv(strings.ToUpper(k)); v != "" {
		return v
	}
	return os.Getenv(strings.ToLower(k))
}
