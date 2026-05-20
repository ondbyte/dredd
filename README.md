# dredd

A Judge0 alternative in Go. Runs untrusted user code inside Firecracker MicroVMs.

- Docker is used only **at setup time** to build per-language rootfs images.
- The running server has no Docker dependency.
- Async API: `POST /exec` returns a job id; `GET /status/{id}` returns per-test-case results.

## Binaries

| Binary | Purpose |
|---|---|
| `dredd` | HTTP API + queue worker |
| `dredd-build` | Setup CLI: turn a Docker image into a Firecracker rootfs and register the language |
| `dreddagent` | Guest-side init binary, copied into each rootfs by `dredd-build` |

## Build

```bash
go build -o bin/dredd ./cmd/dredd
go build -o bin/dredd-build ./cmd/dredd-build
CGO_ENABLED=0 go build -o bin/dreddagent ./guest/dreddagent
```

### Docker

The provided `Dockerfile` produces two images via target selection:

```bash
# Runtime image — ships `dredd` + `dreddagent`, no Docker daemon needed.
docker build --target dredd        -t dredd:latest        .

# Setup-only image — ships `dredd-build` plus docker CLI, mkfs.ext4, mount.
docker build --target dredd-build  -t dredd-build:latest  .
```

The runtime image still needs the host's Firecracker binary and a `vmlinux`
kernel bind-mounted in; the dredd-build image needs `/var/run/docker.sock`
and privileged mode (loop mount).

## Tests

The integration suite spins the whole infrastructure up in-process for every
test case — miniredis for Redis and `dreddtest.LocalExecutor` in place of
the Firecracker pool — runs the test, then tears everything down via
`t.Cleanup`.

```bash
go test ./...
```

The suite covers: `/languages`, multi-stdin `/exec` → `/status`, the
compile step, unknown-language and missing-id error paths, single-retry on
VM failure, fail-after-two-VM-failures, and TTL-based result expiry.

### Language matrix

The language catalogue lives in
[dredd_languages_test.go](./dredd_languages_test.go) and enumerates the
full Judge0 language set — Assembly (NASM), Bash, FreeBASIC, C (7 GCC + 2
Clang versions), C++ (4 GCC + 1 Clang), C# (Mono), Clojure, COBOL, Common
Lisp (SBCL), D, Dart, Elixir, Erlang, F#, Fortran, Go (×4), Groovy,
Haskell, Java (×2), JavaFX, Node.js (×4), Kotlin (×2), Lua, Objective-C,
OCaml, Octave, Pascal, Perl, PHP (×2), Prolog, Python (×6), R (×2), Ruby,
Rust (×2), Scala (×2), SQLite, Swift, TypeScript (×3), Visual Basic.NET.

Two test variants run the catalogue:

#### `TestEndToEnd_AllLanguages` — host PATH, best-effort

Uses `dreddtest.LocalExecutor` to run each case directly on the host via
`/bin/sh -c`. Each entry probes for its required binary via
`exec.LookPath`; if absent the subtest is skipped. Runs as part of
`go test ./...`.

#### `TestEndToEnd_AllLanguagesDocker` — non-negotiable, every language must pass

Uses `dreddtest.DockerExecutor` ([dreddtest/docker_executor.go](./dreddtest/docker_executor.go))
to run each case inside a transient `docker run` against its declared
image (e.g. `python:3.12`, `gcc:13`, `rust:1.85`). Mirrors dredd's
per-rootfs isolation model. **There are no skips** — if Docker is missing,
an image can't be pulled, or any case produces unexpected stdout, the
test fails.

Gated behind `DREDD_DOCKER_LANG_TEST=1` because (a) it requires the
Docker daemon and (b) it pulls many GB of language images on first run.

Five languages don't have a usable public Docker image (Assembly/NASM,
GnuCOBOL, FreeBASIC, Kotlin 1.3, Objective-C with GNU runtime, Free
Pascal Compiler). For these, [dreddtest/images/](./dreddtest/images/)
contains a small Dockerfile per language and a build helper.

#### One-shot all-languages run

```bash
# (recommended) Authenticate to avoid Docker Hub's anonymous pull-rate cap.
# The all-languages test pulls many GB of images; without an authenticated
# daemon you will hit "toomanyrequests" mid-run.
docker login

# Build the custom dredd-test/* images (one-time, ~5–10 min):
./dreddtest/images/build.sh

# Run the matrix (pre-pulls every public image; ~10 GB on first run):
DREDD_DOCKER_LANG_TEST=1 DREDD_DOCKER_PREWARM=1 go test \
    -run AllLanguagesDocker -timeout 60m -v ./...

# Or via Makefile (does both):
make test-languages
```

For development, a single language at a time:

```bash
DREDD_DOCKER_LANG_TEST=1 go test -run AllLanguagesDocker/python-3.12 -v ./...
```

## Embedding dredd in another Go program

dredd is importable as a library. The top-level [`dredd`](./dredd.go)
package exposes an `App` you can wire up yourself:

```go
import (
    "context"

    "github.com/ondbyte/dredd"
    "github.com/ondbyte/dredd/config"
    "github.com/ondbyte/dredd/langs"
    "github.com/redis/go-redis/v9"
)

cfg, _   := config.FromEnv()
reg, _   := langs.Load(cfg.LanguagesFile, cfg.RootfsDir)
rdb      := redis.NewClient(&redis.Options{Addr: "localhost:6379"})

app, err := dredd.New(dredd.Options{Config: cfg, Registry: reg, Redis: rdb})
if err != nil { panic(err) }
defer app.Shutdown(context.Background())

go app.Run(context.Background())
```

To swap the Firecracker-backed executor (e.g. for tests or a different
isolation backend) pass `Options.Executor`. Any type that implements
[`worker.Executor`](./worker/worker.go) works.

## One-time setup

Prereqs: KVM-capable host, a Firecracker `vmlinux` kernel, Docker (for `dredd-build` only), Redis, and `firecracker` on `$PATH`.

```bash
sudo mkdir -p /var/lib/dredd/{rootfs,kernel}
sudo cp bin/dreddagent /usr/local/bin/
# Place your Firecracker kernel here:
sudo cp vmlinux /var/lib/dredd/kernel/vmlinux

export DREDD_AGENT_BINARY=/usr/local/bin/dreddagent
export DREDD_LANGUAGES_FILE=/var/lib/dredd/languages.json
export DREDD_ROOTFS_DIR=/var/lib/dredd/rootfs

sudo -E ./bin/dredd-build add \
  --image python:3.12 --id python-3.12 \
  --name Python --version 3.12 \
  --source main.py --run "python3 /work/main.py"

sudo -E ./bin/dredd-build add \
  --image gcc:13 --id gcc-13 \
  --name "C (gcc)" --version 13 \
  --source main.c \
  --compile "gcc /work/main.c -O2 -o /work/a.out" \
  --run "/work/a.out"
```

## Run

```bash
export DREDD_REDIS_URL=redis://localhost:6379/0
export DREDD_KERNEL_PATH=/var/lib/dredd/kernel/vmlinux
export DREDD_LANGUAGES_FILE=/var/lib/dredd/languages.json
export DREDD_ROOTFS_DIR=/var/lib/dredd/rootfs
export DREDD_POOL_STRATEGY=prewarmed_per_lang   # or ondemand_spare, prewarmed_generic
export DREDD_POOL_SIZE=2
./bin/dredd
```

## API

### `GET /languages`

```json
[{"id":"python-3.12","name":"Python","version":"3.12"}]
```

### `POST /exec`

```json
{
  "language": "python-3.12",
  "source": "import sys\nprint(int(sys.stdin.read())*2)",
  "stdins": ["1", "2", "3"],
  "time_limit_ms": 2000,
  "memory_limit_mb": 256
}
```

Response `202 Accepted`:

```json
{"id": "01JF..."}
```

### `GET /status/{id}`

```json
{
  "id": "01JF...",
  "language": "python-3.12",
  "status": "done",
  "results": [
    {"stdout":"2\n","stderr":"","exit_code":0,"time_ms":0,"memory_kb":0,"timed_out":false},
    {"stdout":"4\n","stderr":"","exit_code":0,"time_ms":0,"memory_kb":0,"timed_out":false},
    {"stdout":"6\n","stderr":"","exit_code":0,"time_ms":0,"memory_kb":0,"timed_out":false}
  ]
}
```

A terminal status (`done` or `failed`) sets a TTL of `DREDD_RESULT_TTL_SECONDS` (default 300 s) on the job hash.

## Environment

| Var | Required | Default | Meaning |
|---|---|---|---|
| `DREDD_HTTP_ADDR` |  | `:8080` | API listen address |
| `DREDD_REDIS_URL` |  | `redis://localhost:6379/0` | Redis connection |
| `DREDD_LANGUAGES_FILE` | yes |  | JSON file of supported languages |
| `DREDD_KERNEL_PATH` | yes |  | Firecracker `vmlinux` |
| `DREDD_ROOTFS_DIR` |  | `/var/lib/dredd/rootfs` | Where per-language ext4 files live |
| `DREDD_AGENT_BINARY` | (build) |  | Host path to `dreddagent`, used by `dredd-build` |
| `DREDD_POOL_STRATEGY` |  | `prewarmed_per_lang` | `prewarmed_per_lang` \| `ondemand_spare` \| `prewarmed_generic` |
| `DREDD_POOL_SIZE` |  | `1` | Idle VMs per language (or total, for `prewarmed_generic`) |
| `DREDD_GENERIC_ROOTFS` | iff strategy=`prewarmed_generic` |  | Path to multi-toolchain ext4 image |
| `DREDD_WORKER_CONCURRENCY` |  | `4` | Parallel job processors |
| `DREDD_RESULT_TTL_SECONDS` |  | `300` | TTL on completed job hashes |
| `DREDD_VM_VCPUS` |  | `1` | vCPUs per MicroVM |
| `DREDD_VM_MEM_MB` |  | `256` | Memory per MicroVM |
| `DREDD_DEFAULT_TIME_LIMIT_MS` |  | `2000` | Per-test-case fallback time limit |
| `DREDD_DEFAULT_MEMORY_LIMIT_MB` |  | `256` | Per-test-case fallback memory limit |
| `DREDD_OUTPUT_LIMIT_BYTES` |  | `1048576` | Cap on stdout/stderr per case |
| `DREDD_FIRECRACKER_BIN` |  | `firecracker` | Firecracker binary on `$PATH` |

## Architecture

```
client ── /exec ──▶ api ──LPUSH──▶ Redis queue ──BRPOP──▶ worker
                                                            │ Acquire
                                                            ▼
                                                          VMPool
                                                            │
                                                            ▼
                                                       Firecracker VM
                                                       └ dreddagent (vsock)
                                                            │
                                                          results
                                                            ▼
client ◀── /status ── api ◀──HGETALL── Redis hash ◀──HSET── worker
```

On VM failure (vsock dial error, agent timeout, process crash) the worker calls `pool.Discard(vm)` and retries the job exactly once on a fresh VM before marking it `failed`.

## Pool strategies

- **`prewarmed_per_lang`** — Keep N booted VMs per language. Lowest latency, highest memory.
- **`ondemand_spare`** — Boot a fresh VM per request, plus one hot spare per language as a retry buddy. Lower memory, ~boot-time latency.
- **`prewarmed_generic`** — Single pool of generic VMs booted off `DREDD_GENERIC_ROOTFS`; the language toolchain must already be present in that rootfs (or the run command must self-bootstrap). Best when there are many languages with sparse traffic.
