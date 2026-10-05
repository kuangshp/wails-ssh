// transferbench measures the production transfer backend against a disposable,
// encrypted loopback SSH/SFTP server. It never opens saved connection profiles.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"
)

type config struct {
	Files    int           `json:"files_per_session"`
	Size     int64         `json:"bytes_per_file"`
	Sessions int           `json:"sessions"`
	Runs     int           `json:"-"`
	Delay    time.Duration `json:"-"`
	Timeout  time.Duration `json:"-"`
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "transferbench:", err)
		os.Exit(1)
	}
}

func execute() error {
	var cfg config
	flag.IntVar(&cfg.Files, "files", 1178, "number of files uploaded by each session")
	flag.Int64Var(&cfg.Size, "size", 16384, "bytes per file")
	flag.IntVar(&cfg.Sessions, "sessions", 1, "concurrent sessions, each with a separate destination")
	flag.IntVar(&cfg.Runs, "runs", 1, "number of independent runs")
	flag.DurationVar(&cfg.Delay, "delay", 0, "extra SFTP reply latency, e.g. 0, 20ms or 80ms; requests overlap")
	flag.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "deadline for each run, including source generation and verification")
	flag.Parse()
	if flag.NArg() != 0 || cfg.Files < 1 || cfg.Size < 0 || cfg.Sessions < 1 || cfg.Runs < 1 || cfg.Delay < 0 || cfg.Timeout <= 0 {
		return fmt.Errorf("files, sessions, runs and timeout must be positive; size and delay must be nonnegative; no positional arguments are accepted")
	}
	const maxInt64 = int64(1<<63 - 1)
	if cfg.Files > int(^uint(0)>>1)/cfg.Sessions {
		return fmt.Errorf("total file count exceeds int")
	}
	if cfg.Size != 0 && (int64(cfg.Files) > maxInt64/cfg.Size || int64(cfg.Sessions) > maxInt64/(int64(cfg.Files)*cfg.Size)) {
		return fmt.Errorf("total byte count exceeds int64")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	encoder := json.NewEncoder(os.Stdout)
	for run := 1; run <= cfg.Runs; run++ {
		result, err := benchmark(ctx, cfg, run)
		if err != nil {
			result.Error = err.Error()
		}
		// benchmark has returned through all cleanup defers. These values include
		// its retained JSON verification records, but no open fixture resources.
		runtime.GC()
		var cleaned runtime.MemStats
		runtime.ReadMemStats(&cleaned)
		result.Runtime.AfterCleanupGCHeapBytes = cleaned.HeapAlloc
		result.Runtime.AfterCleanupGoroutines = runtime.NumGoroutine()
		if encodeErr := encoder.Encode(result); encodeErr != nil {
			return encodeErr
		}
		if err != nil {
			return err
		}
		result = runResult{} // Do not retain the previous run's file-hash records.
	}
	return nil
}
