package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Serves a 10GB stream of zeros
type ZeroReader struct {
	Size   int64
	Offset int64
}

func (z *ZeroReader) Read(p []byte) (n int, err error) {
	if z.Offset >= z.Size {
		return 0, io.EOF
	}
	remaining := z.Size - z.Offset
	if int64(len(p)) > remaining {
		n = int(remaining)
	} else {
		n = len(p)
	}
	// Memory is potentially dirty if reused, strict zeroing
	for i := 0; i < n; i++ {
		p[i] = 0
	}
	z.Offset += int64(n)
	return n, nil
}

func (z *ZeroReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = z.Offset + offset
	case io.SeekEnd:
		newOffset = z.Size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if newOffset < 0 {
		return 0, errors.New("negative position")
	}
	z.Offset = newOffset
	return newOffset, nil
}

// Concurrency limiter
var activeConnections int32

const maxConnections = 32

func limitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&activeConnections, 1)
		defer atomic.AddInt32(&activeConnections, -1)

		if current > maxConnections {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func main() {
	http.HandleFunc("/2GB.bin", limitMiddleware(func(w http.ResponseWriter, r *http.Request) {
		const size = 2 * 1024 * 1024 * 1024 // 2GB
		w.Header().Set("Content-Length", "2147483648")
		w.Header().Set("Content-Type", "application/octet-stream")

		http.ServeContent(w, r, "2GB.bin", time.Now(), &ZeroReader{Size: size})
	}))

	log.Println("Benchmark server running on :8080 (Max 32 connections)...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
