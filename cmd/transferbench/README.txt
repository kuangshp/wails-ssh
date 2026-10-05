transferbench: reproducible production-backend upload benchmark

Build from the repository root:
  go build -o /tmp/wails-transferbench-final ./cmd/transferbench

Examples (run serially, without other load tests):
  /tmp/wails-transferbench-final -files 1178 -size 16384 -delay 0 -runs 1
  /tmp/wails-transferbench-final -files 1178 -size 16384 -delay 20ms -runs 1
  /tmp/wails-transferbench-final -files 1178 -size 16384 -delay 80ms -runs 1
  /tmp/wails-transferbench-final -files 1 -size 268435456 -sessions 1 -runs 3
  /tmp/wails-transferbench-final -files 1178 -size 16384 -sessions 4 -runs 3

On macOS, measure the whole process separately:
  /usr/bin/time -l /tmp/wails-transferbench-final -files 1178 -size 16384 > result.jsonl

Default flags: files=1178, size=16384 bytes, sessions=1, runs=1,
delay=0, timeout=10m. A run exceeding timeout fails; Ctrl-C also cancels and
cleans up. Each session uploads the same generated payload to a distinct target.
Each run has an independent fixture and fresh destination; existing files never
trigger overwrite prompts. Files have deterministic, distinct pseudo-random
contents, streamed without allocating the entire payload in memory.

The benchmark uses a real 127.0.0.1 TCP listener, randomly generated SSH host
key and password, SSH encryption, github.com/pkg/sftp's filesystem server,
production sshclient.Manager.Connect/TransferDialer, and transfer.Service.Upload.
It does not start an interactive terminal, a WebView, or the desktop application.
Connection profiles, saved passwords, private keys and business files are not
opened. The production host-key checker may normally read ~/.ssh/known_hosts;
the generated server key is pinned only in the fixture's separate known_hosts.
The test never changes the user's trust file and never connects off loopback.

All generated source/destination/trust files and server connections are removed
after a run, including timeout/cancellation. Production Upload also creates and
removes its own private snapshot. SIGKILL or a process crash cannot run cleanup.

One JSON object is emitted per run. Fields:
  wall_seconds: first measured Manager.Connect through completion of all uploads.
    Includes SSH authentication, SFTP initialization, upload source snapshots,
    directory setup, per-file permissions and atomic replacement. Source fixture
    generation, final SHA verification, JSON encoding and teardown are excluded.
  effective_mib_per_second: total successful payload bytes / wall_seconds / 2^20.
    Zero on a failed run; session_results gives any bytes/files actually copied.
  prepare_to_first_dial_seconds: each session's first "preparing" progress event
    through its first transfer dial invocation. This includes source collection,
    snapshot and final source checks, excludes the original login, and ends
    before directory-transfer authentication. -1 means this phase did not finish.
  source_setup_seconds / verification_seconds: excluded phases, reported separately.
  runtime: samples heap and goroutines every 20ms during the measured interval;
    total_alloc_bytes, gc_count and gc_pause_seconds are interval deltas.
    Peak values are sampled, not exact maxima. Runtime includes BOTH production
    transfer backend and benchmark SSH/SFTP server, plus sampling overhead.
    after_cleanup_gc_heap_bytes and after_cleanup_goroutines are captured after
    the benchmark returned through cleanup and an explicit GC. The heap still
    includes that run's per-file verification records awaiting JSON encoding.
    GC is also requested before each measured interval; heap limits are unchanged.
  progress_events: number of production transfer:progress events.
  ssh_connections_peak / total: authenticated SSH connections, including original
    login connections, directory setup connections and transfer workers.
  session_results: status, file/byte accounting, preparation time and transfer
    dial count, indexed in the same order as -sessions.
  verification: actual SHA-256 for EVERY uploaded file, compared to the streamed
    source manifest; failed comparisons include expected_sha256 and/or an error.
    Hashing reads the destination files directly after closing the SSH server.
    Non-completion or a mismatching/missing file produces a nonzero exit status.

Delay model:
  -delay adds the specified latency to SFTP server replies only. SSH authentication
  and SSH keepalive replies are not delayed. Each reply gets an absolute due time
  when enqueued; a bounded queue preserves byte order while overlapping waits.
  There is no per-WRITE serialized sleep. Thus pipelined requests overlap latency.
  This is an application reply-latency simulation, NOT a bandwidth cap, packet-loss
  model, or complete WAN emulation. Report it as "extra SFTP reply delay".

Interpretation limits:
  File writes use the actual filesystem and normal OS caches. No additional
  per-file fsync is imposed; these results are not durable-storage throughput.
  Loopback cannot establish real server compatibility, WAN performance or
  production network reliability. The real server's disk, CPU, SFTP behavior,
  bandwidth and RTT still matter.
  /usr/bin/time -l CPU/RSS covers the ENTIRE process lifetime, including source
  generation, server, transfers, hash verification and JSON encoding. It is not
  the desktop app's memory consumption and is broader than runtime's interval.
