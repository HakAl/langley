package proxy

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

const defaultIdleTimeout = 5 * time.Minute

// tunnel copies data bidirectionally between clientConn and upstreamConn.
// Either side closing or going idle (no reads for idleTimeout) tears down both.
func tunnel(clientConn, upstreamConn net.Conn, logger *slog.Logger, host string) {
	tunnelWithTimeout(clientConn, upstreamConn, logger, host, defaultIdleTimeout)
}

// tunnelWithTimeout is the testable core that accepts an explicit idle timeout.
func tunnelWithTimeout(clientConn, upstreamConn net.Conn, logger *slog.Logger, host string, idleTimeout time.Duration) {
	logger.Debug("tunnel established", "host", host)

	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			clientConn.Close()
			upstreamConn.Close()
			logger.Debug("tunnel closed", "host", host)
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// client -> upstream
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(upstreamConn, clientConn, idleTimeout)
		closeAll()
	}()

	// upstream -> client
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(clientConn, upstreamConn, idleTimeout)
		closeAll()
	}()

	wg.Wait()
}

// copyWithIdleTimeout copies from src to dst, resetting a read deadline on src
// after every successful read. If no data arrives within idleTimeout, the copy
// stops and the caller tears down both sides.
func copyWithIdleTimeout(dst io.Writer, src net.Conn, idleTimeout time.Duration) {
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		n, err := src.Read(buf)
		if n > 0 {
			if _, wErr := dst.Write(buf[:n]); wErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
