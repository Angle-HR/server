package uploads

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Verdict is the result of a scan.
type Verdict struct {
	Clean     bool
	Signature string // what was found, when not clean
}

// Scanner checks a file for malware. An error means the file could not be checked, which is never the same as
// clean: callers fail closed.
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
}

// ClamAV scans through a clamd daemon with the INSTREAM command over TCP.
type ClamAV struct {
	Addr    string        // host:port of clamd
	Timeout time.Duration // whole scan; default 60 seconds
	Dialer  func(ctx context.Context, network, addr string) (net.Conn, error)
}

const clamChunk = 32 * 1024

// ErrScannerUnavailable is returned when clamd cannot be reached or gives no usable answer.
var ErrScannerUnavailable = errors.New("uploads: malware scanner unavailable")

// Scan streams r to clamd and returns its verdict.
func (c *ClamAV) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := c.Dialer
	if dial == nil {
		d := &net.Dialer{}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", c.Addr)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if _, err = conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
	}
	buf := make([]byte, clamChunk)
	var lenBuf [4]byte
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(lenBuf[:], uint32(n))
			if _, err = conn.Write(lenBuf[:]); err != nil {
				return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
			}
			if _, err = conn.Write(buf[:n]); err != nil {
				return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Verdict{}, fmt.Errorf("uploads: read file for scan: %w", rerr)
		}
	}
	binary.BigEndian.PutUint32(lenBuf[:], 0)
	if _, err = conn.Write(lenBuf[:]); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
	}

	resp, err := io.ReadAll(io.LimitReader(conn, 4096))
	if err != nil && len(resp) == 0 {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScannerUnavailable, err)
	}
	return parseReply(string(resp))
}

// parseReply reads clamd's answer: "stream: OK", "stream: <name> FOUND" or "... ERROR".
func parseReply(reply string) (Verdict, error) {
	reply = strings.TrimSpace(strings.Trim(reply, "\x00"))
	switch {
	case strings.HasSuffix(reply, " OK"):
		return Verdict{Clean: true}, nil
	case strings.HasSuffix(reply, " FOUND"):
		sig := strings.TrimSuffix(strings.TrimPrefix(reply, "stream:"), " FOUND")
		return Verdict{Signature: strings.TrimSpace(sig)}, nil
	}
	return Verdict{}, fmt.Errorf("%w: unexpected reply %q", ErrScannerUnavailable, truncate(reply, 80))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
