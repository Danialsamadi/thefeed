package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/sartoopjj/thefeed/internal/protocol"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// privateChannelID derives a short, stable channel ID from an invite
// hash. Keeps Feed channel Names and DNS metadata small while still
// preserving cache stability across file reorders. 4-byte SHA-256
// prefix; collision space ~4 billion, more than enough per server.
func privateChannelID(inviteHash string) string {
	sum := sha256.Sum256([]byte(inviteHash))
	return "pv:" + hex.EncodeToString(sum[:4])
}

// LoadPrivateInvites reads invite links from path (one per line, `#`
// comments allowed) and returns the parsed hashes. Missing file is
// not an error — the feature is opt-in.
func LoadPrivateInvites(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("[server] close private invites file: %v", err)
		}
	}()

	var hashes []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hash, err := serverconfig.ParseInviteHash(line)
		if err != nil {
			log.Printf("[server] private_channels.txt:%d: %v", lineNum, err)
			continue
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true
		hashes = append(hashes, hash)
	}
	return hashes, scanner.Err()
}

// privatePeers holds resolved invite hashes after a successful join.
// Keyed by invite hash; the value is the InputPeerChannel the fetch
// loop uses to call MessagesGetHistory.
type privatePeers struct {
	mu      sync.RWMutex
	byHash  map[string]*resolvedPeer
	ordered []string // hashes in declaration order, drives fetch ordering
}

func newPrivatePeers(hashes []string) *privatePeers {
	pp := &privatePeers{byHash: make(map[string]*resolvedPeer)}
	for _, h := range hashes {
		pp.ordered = append(pp.ordered, h)
	}
	return pp
}

func (p *privatePeers) get(hash string) (*resolvedPeer, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.byHash[hash]
	return r, ok
}

func (p *privatePeers) set(hash string, r *resolvedPeer) {
	p.mu.Lock()
	p.byHash[hash] = r
	p.mu.Unlock()
}

// joinOrCheckInvite returns the InputPeerChannel for a private
// channel, joining it via importChatInvite if not already a member.
func joinOrCheckInvite(ctx context.Context, api *tg.Client, hash string) (*resolvedPeer, error) {
	check, err := api.MessagesCheckChatInvite(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("check invite %q: %w", hash, err)
	}

	// chatInviteAlready: we're a member. Use the embedded chat directly.
	if already, ok := check.(*tg.ChatInviteAlready); ok {
		return chatToResolvedPeer(already.Chat, "private channel")
	}
	// chatInvitePeek: peek (preview) — usually means we need to join.
	// Fall through to ImportChatInvite below.

	// Not a member yet → import (join).
	updates, err := api.MessagesImportChatInvite(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("import invite %q: %w", hash, err)
	}
	// Joining used to answer with Updates directly. It now answers with a
	// RESULT that either carries those Updates or says the invite needs a
	// bot's web view first -- which is not something a headless reader can
	// complete, so it is reported rather than silently returning nothing.
	ok, isOK := updates.(*tg.MessagesChatInviteJoinResultOk)
	if !isOK {
		return nil, fmt.Errorf("import invite %q: the invite needs to be accepted "+
			"interactively (%T), which this reader cannot do", hash, updates)
	}
	// Updates can be one of several shapes; we look for embedded chats.
	chats := extractChatsFromUpdates(ok.Updates)
	if len(chats) == 0 {
		return nil, fmt.Errorf("import invite %q: no chat in response", hash)
	}
	return chatToResolvedPeer(chats[0], "private channel")
}

// chatToResolvedPeer converts a tg.ChatClass to our internal peer
// representation. Only channels/supergroups are supported — legacy
// basic groups (tg.Chat without access hash) are rejected.
func chatToResolvedPeer(c tg.ChatClass, fallbackTitle string) (*resolvedPeer, error) {
	ch, ok := c.(*tg.Channel)
	if !ok {
		return nil, fmt.Errorf("invite resolved to non-channel %T (basic groups not supported)", c)
	}
	title := strings.TrimSpace(ch.Title)
	if title == "" {
		title = fallbackTitle
	}
	canSend := !ch.Broadcast || ch.Creator || ch.AdminRights.PostMessages
	pid, _ := extractChatPhotoID(ch.Photo)
	return &resolvedPeer{
		peer: &tg.InputPeerChannel{
			ChannelID:  ch.ID,
			AccessHash: ch.AccessHash,
		},
		chatType: protocol.ChatTypeChannel,
		canSend:  canSend,
		title:    title,
		photoID:  pid,
	}, nil
}

// extractChatsFromUpdates pulls tg.ChatClass values out of any of the
// gotd Updates variants. ImportChatInvite returns
// tg.UpdatesClass — concretely usually *tg.Updates which has .Chats.
func extractChatsFromUpdates(updates tg.UpdatesClass) []tg.ChatClass {
	switch u := updates.(type) {
	case *tg.Updates:
		return u.Chats
	case *tg.UpdatesCombined:
		return u.Chats
	}
	return nil
}

// resolveAllPrivate resolves every queued invite hash, logging and
// skipping individual failures. betweenCalls paces requests below
// Telegram's FLOOD_WAIT threshold.
func (p *privatePeers) resolveAllPrivate(ctx context.Context, api *tg.Client, betweenCalls time.Duration) {
	for i, hash := range p.ordered {
		if ctx.Err() != nil {
			return
		}
		if _, ok := p.get(hash); ok {
			continue
		}
		rp, err := joinOrCheckInvite(ctx, api, hash)
		if err != nil {
			log.Printf("[telegram] private channel %d/%d (hash=%s): %v",
				i+1, len(p.ordered), hash, err)
			continue
		}
		p.set(hash, rp)
		log.Printf("[telegram] joined private channel %q (id=%d, hash=%s)",
			rp.title, rp.peer.(*tg.InputPeerChannel).ChannelID, hash)
		if betweenCalls > 0 && i < len(p.ordered)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(betweenCalls):
			}
		}
	}
}
