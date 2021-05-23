package tweeter

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	H_LIMIT              = "X-Rate-Limit-Limit"
	H_LIMIT_REMAIN       = "X-Rate-Limit-Remaining"
	H_LIMIT_RESET        = "X-Rate-Limit-Reset"
	H_MEDIA_LIMIT        = "X-MediaRateLimit-Limit"
	H_MEDIA_LIMIT_REMAIN = "X-MediaRateLimit-Remaining"
	H_MEDIA_LIMIT_RESET  = "X-MediaRateLimit-Reset"
)

const (
	STATUS_OK           = 200
	STATUS_CREATED      = 201
	STATUS_ACCEPTED     = 202
	STATUS_NO_CONTENT   = 204
	STATUS_INVALID      = 400
	STATUS_UNAUTHORIZED = 401
	STATUS_FORBIDDEN    = 403
	STATUS_NOTFOUND     = 404
	STATUS_LIMIT        = 429
	STATUS_GATEWAY      = 502
)

// Error returned if there was an issue parsing the response body.
type ResponseError struct {
	Body string
	Code int
}

func NewResponseError(code int, body string) ResponseError {
	return ResponseError{Code: code, Body: body}
}

func (e ResponseError) Error() string {
	return fmt.Sprintf(
		"Unable to handle response (status code %d): `%v`",
		e.Code,
		e.Body)
}

// -----------------------------------------------------------------------------------------

// RateLimitError is returned from SendRequest when a rate limit is encountered.
type RateLimitError struct {
	Limit     uint32    // the rate limit ceiling for that given endpoint
	Remaining uint32    // the number of requests left for {Limit} window
	Reset     time.Time // the remaining window before the rate limit resets
}

func (e RateLimitError) Error() string {
	msg := "Rate limit: %v, Remaining: %v, Reset: %v"
	return fmt.Sprintf(msg, e.Limit, e.Remaining, e.Reset)
}

func (e RateLimitError) HasRateLimit() bool {
	return true
}

func (e RateLimitError) RateLimit() uint32 {
	return e.Limit
}

func (e RateLimitError) RateLimitRemaining() uint32 {
	return e.Remaining
}

func (e RateLimitError) RateLimitReset() time.Time {
	return e.Reset
}

// -----------------------------------------------------------------------------------------

type Error map[string]interface{}

func (e Error) Code() int64 {
	return int64(float64Value(e, "code"))
}

func (e Error) Message() string {
	return stringValue(e, "message")
}

func (e Error) Error() string {
	return fmt.Sprintf("Error %v: %v", e.Code(), e.Message())
}

type Errors map[string]interface{}

func (e Errors) Error() string {
	var (
		msg  string = ""
		err  Error
		ok   bool
		errs []interface{}
	)
	errs = arrayValue(e, "errors")
	if len(errs) == 0 {
		return msg
	}
	for _, val := range errs {
		if err, ok = val.(map[string]interface{}); ok {
			msg += err.Error() + ". "
		}
	}
	return msg
}

func (e Errors) String() string {
	return e.Error()
}

func (e Errors) Errors() []Error {
	var errs = arrayValue(e, "errors")
	var out = make([]Error, len(errs))
	for i, val := range errs {
		out[i] = Error(val.(map[string]interface{}))
	}
	return out
}

// -----------------------------------------------------------------------------------------

// ApiResponse provides methods for retrieving information from the HTTP
// headers in a Twitter API response.
type ApiResponse http.Response

func (r ApiResponse) HasRateLimit() bool {
	return r.Header.Get(H_LIMIT) != ""
}

func (r ApiResponse) RateLimit() uint32 {
	h := r.Header.Get(H_LIMIT)
	i, _ := strconv.ParseUint(h, 10, 32)
	return uint32(i)
}

func (r ApiResponse) RateLimitRemaining() uint32 {
	h := r.Header.Get(H_LIMIT_REMAIN)
	i, _ := strconv.ParseUint(h, 10, 32)
	return uint32(i)
}

func (r ApiResponse) RateLimitReset() time.Time {
	h := r.Header.Get(H_LIMIT_RESET)
	i, _ := strconv.ParseUint(h, 10, 32)
	t := time.Unix(int64(i), 0)
	return t
}

func (r ApiResponse) HasMediaRateLimit() bool {
	return r.Header.Get(H_MEDIA_LIMIT) != ""
}

func (r ApiResponse) MediaRateLimit() uint32 {
	h := r.Header.Get(H_MEDIA_LIMIT)
	i, _ := strconv.ParseUint(h, 10, 32)
	return uint32(i)
}

func (r ApiResponse) MediaRateLimitRemaining() uint32 {
	h := r.Header.Get(H_MEDIA_LIMIT_REMAIN)
	i, _ := strconv.ParseUint(h, 10, 32)
	return uint32(i)
}

func (r ApiResponse) MediaRateLimitReset() time.Time {
	h := r.Header.Get(H_MEDIA_LIMIT_RESET)
	i, _ := strconv.ParseUint(h, 10, 32)
	t := time.Unix(int64(i), 0)
	return t
}

func (r ApiResponse) GetBodyReader() (io.ReadCloser, error) {
	header := strings.ToLower(r.Header.Get("Content-Encoding"))
	if header == "" || strings.Index(header, "gzip") == -1 {
		return r.Body, nil
	}
	return gzip.NewReader(r.Body)
}

func (r ApiResponse) ReadBody() ([]byte, error) {
	reader, err := r.GetBodyReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ioutil.ReadAll(reader)
}

// Parse unmarshals a JSON encoded HTTP response into the supplied interface,
// with handling for the various kinds of errors the Twitter API can return.
//
// The returned error may be of the type Errors, RateLimitError,
// ResponseError, or an error returned from io.Reader.Read().
func (r ApiResponse) Parse(out interface{}) (err error) {
	var b []byte
	switch r.StatusCode {

	case STATUS_UNAUTHORIZED:
		fallthrough

	case STATUS_NOTFOUND:
		fallthrough

	case STATUS_GATEWAY:
		fallthrough

	case STATUS_FORBIDDEN:
		fallthrough

	case STATUS_INVALID:
		e := &Errors{}
		if b, err = r.ReadBody(); err != nil {
			return
		}
		if err = json.Unmarshal(b, e); err != nil {
			err = NewResponseError(r.StatusCode, string(b))
		} else {
			err = *e
		}
		return

	case STATUS_LIMIT:
		err = RateLimitError{
			Limit:     r.RateLimit(),
			Remaining: r.RateLimitRemaining(),
			Reset:     r.RateLimitReset(),
		}
		// consume the request body even if we don't need it
		r.ReadBody()
		return

	case STATUS_NO_CONTENT:
		return

	case STATUS_CREATED:
		fallthrough

	case STATUS_ACCEPTED:
		fallthrough

	case STATUS_OK:
		reader, err := r.GetBodyReader()
		if err != nil {
			return err
		}
		defer reader.Close()
		dec := json.NewDecoder(reader)
		// dec.UseNumber()
		if err = dec.Decode(out); err != nil && err == io.EOF {
			err = nil
		}
		// if b, err = r.ReadBody(); err != nil {
		//   return
		// }
		// err = json.Unmarshal(b, out)
		// if err == io.EOF {
		//   err = nil
		// }

	default:
		if b, err = r.ReadBody(); err != nil {
			return
		}
		err = NewResponseError(r.StatusCode, string(b))
	}
	return
}
