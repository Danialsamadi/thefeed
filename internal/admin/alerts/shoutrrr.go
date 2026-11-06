package alerts

import (
	"context"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
)

// shoutrrrSender sends through Shoutrrr, giving each send 20 seconds.
type shoutrrrSender struct{}

func (shoutrrrSender) Check(url string) error {
	_, err := shoutrrr.CreateSender(url)
	return err
}

func (shoutrrrSender) Send(url, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return shoutrrr.SendContext(ctx, url, message, nil)
}
