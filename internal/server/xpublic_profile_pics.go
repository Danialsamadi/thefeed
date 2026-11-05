package server

import (
	"context"
	"encoding/xml"
	"log"
	"strings"
	"sync"
)

// Nitter / RSS feeds put the channel image at <rss><channel><image><url>.
type xRSSImageEnvelope struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Image struct {
			URL string `xml:"url"`
		} `xml:"image"`
	} `xml:"channel"`
}

func extractXAvatarURL(body []byte) string {
	var env xRSSImageEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return ""
	}
	return strings.TrimSpace(env.Channel.Image.URL)
}

// fetchXAvatar downloads account's avatar from the address its last RSS
// gave. (nil, nil) when there is none: no RSS came in, or it had no avatar.
func (xr *XPublicReader) fetchXAvatar(ctx context.Context, account string) ([]byte, error) {
	const maxAvatarBytes = 512 * 1024
	xr.mu.RLock()
	avatarURL := xr.avatars[account]
	xr.mu.RUnlock()
	if avatarURL == "" {
		return nil, nil
	}
	return httpGetWithLimit(ctx, xr.client, avatarURL, maxAvatarBytes)
}

// fetchAllXProfilePhotos downloads each account's avatar (via Nitter)
// and merges into the bundle under "x:<handle>" keys.
func (xr *XPublicReader) fetchAllXProfilePhotos(ctx context.Context) {
	xr.mu.RLock()
	accounts := append([]string(nil), xr.accounts...)
	xr.mu.RUnlock()

	pics := make(map[string][]byte, len(accounts))
	current := make(map[string]bool, len(accounts))
	var picsMu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, a := range accounts {
		if ctx.Err() != nil {
			return
		}
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		current["x:"+a] = true
		wg.Add(1)
		go func(account string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := xr.fetchXAvatar(ctx, account)
			if err != nil {
				log.Printf("[x profile-pic] @%s: %v", account, err)
				return
			}
			if len(body) == 0 {
				return
			}
			// "x:" prefix avoids colliding with same-name Telegram channels.
			picsMu.Lock()
			pics["x:"+account] = body
			picsMu.Unlock()
		}(a)
	}
	wg.Wait()
	total, dropped := xr.feed.RefreshProfilePics(pics, current, true)
	if len(pics) > 0 || len(dropped) > 0 {
		log.Printf("[x profile-pic] cycle done: %d fetched, %d dropped, %d total in bundle", len(pics), len(dropped), total)
	}
}
