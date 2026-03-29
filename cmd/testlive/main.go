// testlive writes JSON log lines to a file at 1-second intervals
// so you can test --live mode without fighting shell quoting.
//
// Usage:
//   go run ./cmd/testlive live.log
package main

import (
	"fmt"
	"os"
	"time"
)

var lines = []string{
	`{"timestamp":"%s","level":"info","service":"payment-service","message":"Processing payment","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"warn","service":"payment-service","message":"Connection pool under pressure"}`,
	`{"timestamp":"%s","level":"error","service":"payment-service","message":"Connection pool exhausted","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"error","service":"payment-service","message":"Request timeout after 5000ms","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"error","service":"order-service","message":"Upstream payment-service unavailable","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"error","service":"order-service","message":"Retry 1/3 failed","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"error","service":"order-service","message":"Retry 2/3 failed — marking payment-service degraded","trace_id":"abc123"}`,
	`{"timestamp":"%s","level":"error","service":"notification-svc","message":"Order webhook failed: order-service 503"}`,
}

func main() {
	path := "live.log"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Printf("Writing to %s — one line per second\n", path)

	for _, tmpl := range lines {
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		line := fmt.Sprintf(tmpl, ts)
		fmt.Fprintln(f, line)
		fmt.Println("wrote:", line[:60]+"...")
		time.Sleep(250 * time.Millisecond)
	}

	fmt.Println("done")
}
