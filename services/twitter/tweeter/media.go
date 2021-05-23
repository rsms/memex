package tweeter

import (
	"io"
	"mime/multipart"
	"time"
)

type MediaImage struct {
	ImageType string `json:"image_type"` // e.g. "image/jpeg"
	Width     int    `json:"w"`
	Height    int    `json:"h"`
}

type MediaResponse struct {
	Id           uint64        `json:"media_id"`
	Size         int           `json:"size"`
	ExpiresAfter time.Duration `json:"expires_after_secs"`
	Image        *MediaImage
}

type MediaUpload struct {
	Name       string
	BodyReader io.Reader // either set BodyReader or BodyData
	BodyData   []byte    // either set BodyReader or BodyData
}

func (r *MediaUpload) Send(c *Client) (mr *MediaResponse, err error) {
	endpoint := "https://upload.twitter.com/1.1/media/upload.json"
	res, err := c.POSTForm(endpoint, func(m *multipart.Writer) error {
		mediaWriter, err := m.CreateFormFile("media", r.Name)
		if err != nil {
			return err
		}
		if r.BodyReader != nil {
			if _, err = io.Copy(mediaWriter, r.BodyReader); err != nil {
				return err
			}
		} else {
			if _, err := mediaWriter.Write(r.BodyData); err != nil {
				return err
			}
		}
		return nil
	})

	mr = &MediaResponse{}
	if err = res.Parse(mr); err != nil {
		mr = nil
	} else {
		mr.ExpiresAfter = mr.ExpiresAfter * time.Second
	}
	return
}
