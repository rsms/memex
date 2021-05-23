package extension

import (
	"time"
)

// Timestamp is a millisecond-precision Unix timestamp (in UTC)
type Timestamp int64

// returns a millisecond-precision Unix timestamp (in UTC)
func Now() Timestamp {
	return TimeTimestamp(time.Now())
}

func TimeTimestamp(t time.Time) Timestamp {
	sec := t.Unix() // doesn't care about time zone
	nsec := t.UnixNano() - (sec * 1000000000)
	return Timestamp((sec * 1000) + (nsec / 1000000))
}

func (t Timestamp) Time() time.Time {
	sec := int64(t) / 1000
	nsec := (int64(t) - (sec * 1000)) * 1000000
	return time.Unix(sec, nsec)
}

func (t Timestamp) String() string {
	return t.Time().String()
}
