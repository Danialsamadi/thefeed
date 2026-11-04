package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/sartoopjj/thefeed/internal/admin/dockerapi"
)

// Container is the Journal of the container name's log, read through the
// Docker Engine: stdout and stderr, each line at the time Docker took it.
func Container(docker *dockerapi.Client, name string) Journal { return container{docker, name} }

type container struct {
	docker *dockerapi.Client
	name   string
}

// Read gives the lines as journalctl --output=json would, for parse. A
// line's cursor is its time in Unix nanoseconds and how many newer lines
// share that time, such as "1762246803123456789.1": no later line takes an
// earlier time, so it names the same line on every read.
//
// The Engine is asked for its last tail messages, tail doubling until they
// reach back far enough. It isn't asked for lines until the cursor's time:
// the json-file and local drivers take the tail of the whole log first.
//
// Every read goes back from the newest line, so a page deep in
// the log reads all the lines after it; carry the line's depth in the
// cursor to start tail there if that gets slow.
func (c container) Read(ctx context.Context, cursor string, n int) ([]byte, error) {
	at, same := int64(math.MaxInt64), 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d.%d", &at, &same); err != nil {
			return nil, fmt.Errorf("bad cursor %q", cursor)
		}
	}
	for tail := n + 1; ; tail *= 2 {
		lines, sent, err := c.docker.Logs(ctx, c.name, tail)
		if err != nil {
			return nil, err
		}
		// When the tail cuts the log, its oldest line may be missing the
		// first part of a long line; a longer tail reads it whole.
		if sent == tail {
			lines = lines[1:]
		}
		var out [][]byte
		for i, k := len(lines)-1, 0; i >= 0 && len(out) < n; i-- {
			l := lines[i]
			if i+1 < len(lines) && l.At.Equal(lines[i+1].At) {
				k++
			} else {
				k = 0
			}
			if ns := l.At.UnixNano(); ns > at || ns == at && k < same {
				continue
			}
			e, _ := json.Marshal(map[string]string{
				"__CURSOR":             fmt.Sprintf("%d.%d", l.At.UnixNano(), k),
				"__REALTIME_TIMESTAMP": strconv.FormatInt(l.At.UnixMicro(), 10),
				"MESSAGE":              l.Text,
			})
			out = append(out, e)
		}
		if len(out) == n || sent < tail {
			return bytes.Join(out, []byte("\n")), nil
		}
	}
}
