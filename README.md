# dredd — Self-Hosted Code Execution Sandbox in Go

> A fast, secure, **Judge0 alternative** written in Go. Run untrusted user code in **Firecracker MicroVMs** with hardware-level isolation, a simple HTTP API, and built-in support for 40+ programming languages.

[![Go](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)
[![Isolation: Firecracker](https://img.shields.io/badge/Isolation-Firecracker-orange)](https://firecracker-microvm.github.io/)
[![Status: Beta](https://img.shields.io/badge/Status-Beta-blue)](#status)

**dredd** is an open-source **online code execution engine** for online judges, coding interview platforms, automated graders, language playgrounds, AI coding agents, and any application that needs to run **untrusted user-submitted code** safely. It uses [Firecracker](https://firecracker-microvm.github.io/) MicroVMs — the same isolation technology that powers AWS Lambda and Fly.io — to give each submission its own ~125 ms-boot, KVM-backed virtual machine.

---

## Why dredd?

- **Hardware isolation.** Each request runs inside a Firecracker MicroVM, not a Docker container. KVM-enforced separation, not just kernel namespaces.
- **Judge0-style API.** `POST /exec` → `GET /status/{id}`. Drop-in mental model for anyone migrating from [Judge0](https://judge0.com/).
- **40+ languages out of the box.** C/C++ (multiple GCC and Clang versions), Python (2.7 through 3.14), Node.js, Go, Rust, Java, Kotlin, Swift, Ruby, PHP, TypeScript, Scala, Haskell, OCaml, Erlang, Elixir, Bash, Assembly, COBOL, SQL, and more — see the [full matrix](#supported-languages).
- **Async by default.** Submit code, get a job ID, poll for the result. Scales to many concurrent requests on a Redis queue.
- **Multi-test-case execution.** Compile once, run against N stdin inputs sequentially — the workflow every online judge needs.
- **Single Go binary at runtime.** No Docker daemon required to serve traffic. Docker is only used during **setup** to build language rootfs images.
- **Embeddable.** dredd is a Go library. Import it, wire your own queue, drop in a custom executor — full programmatic control.
- **Production-tested resilience.** Single-retry on VM failure, configurable VM pool strategies, automatic result TTL in Redis, request-body size caps.

---

## Quickstart

### Run with Docker

```bash
# Pull or build the dredd runtime image (no Docker daemon needed at runtime).
docker build --target dredd       -t dredd:latest       .
docker build --target dredd-build -t dredd-build:latest .

# Start Redis (required) and dredd:
docker run -d --name dredd-redis redis:7-alpine
docker run -d --name dredd \
    --link dredd-redis:redis \
    -e DREDD_REDIS_URL=redis://redis:6379/0 \
    -v /var/lib/dredd:/var/lib/dredd \
    -p 8080:8080 \
    dredd:latest
```

### Submit code

```bash
# POST /exec — returns a job ID immediately.
curl -X POST http://localhost:8080/exec -H 'Content-Type: application/json' -d '{
  "language": "python-3.12",
  "source":   "import sys\nprint(int(sys.stdin.read()) * 2)",
  "stdins":   ["1", "2", "3"]
}'
# → {"id":"01JF...ULID"}

# GET /status/{id} — fetches the result; per-test-case array.
curl http://localhost:8080/status/01JF...ULID
# → {"id":"01JF...","status":"done","results":[
#      {"stdout":"2\n","exit_code":0,...},
#      {"stdout":"4\n","exit_code":0,...},
#      {"stdout":"6\n","exit_code":0,...}
#    ]}
```

That's the entire mental model.

### Use dredd in your `docker-compose.yml`

Drop dredd into your project's compose stack as a sandboxed code-execution
service that the rest of your app calls over HTTP.

**Host prerequisites** (one-time, on every host that will run the dredd
container):

1. **KVM access.** dredd boots Firecracker MicroVMs, so `/dev/kvm` must
   exist and your user must be able to open it (`sudo chmod 666 /dev/kvm`
   or add the runtime user to the `kvm` group).
2. **Kernel + rootfs files** on the host at `/var/lib/dredd/`. Produce
   them once with `dredd-build` (see [One-time setup](#one-time-setup))
   or download pre-built artifacts from your release pipeline. The
   container mounts this directory read-only.

**Minimal compose snippet** (`docker-compose.yml`):

```yaml
services:
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    volumes:
      - dredd-redis:/data

  dredd:
    image: ghcr.io/ondbyte/dredd:latest   # or build the local Dockerfile
    # build:
    #   context: https://github.com/ondbyte/dredd.git
    #   target: dredd
    restart: unless-stopped
    depends_on:
      - redis
    environment:
      DREDD_HTTP_ADDR: ":8080"
      DREDD_REDIS_URL: "redis://redis:6379/0"
      DREDD_KERNEL_PATH: "/var/lib/dredd/kernel/vmlinux"
      DREDD_LANGUAGES_FILE: "/var/lib/dredd/languages.json"
      DREDD_ROOTFS_DIR: "/var/lib/dredd/rootfs"
      DREDD_POOL_STRATEGY: "prewarmed_per_lang"
      DREDD_POOL_SIZE: "2"
      DREDD_WORKER_CONCURRENCY: "4"
    volumes:
      - /var/lib/dredd:/var/lib/dredd:ro    # kernel + rootfs (built once)
    devices:
      - /dev/kvm:/dev/kvm                   # Firecracker needs KVM
    cap_add:
      - NET_ADMIN                           # tap-device setup if you use networking
      - SYS_RESOURCE                        # rlimits for guest processes
    # If your host kernel / Docker version refuses to pass /dev/kvm without
    # full privileges, swap the cap_add/devices lines for:
    # privileged: true
    expose:
      - "8080"                              # internal to the compose network

  # Your own application service — talks to dredd via the compose network.
  app:
    image: yourorg/yourapp:latest
    depends_on:
      - dredd
    environment:
      DREDD_URL: "http://dredd:8080"        # call dredd from your code here
    ports:
      - "3000:3000"

volumes:
  dredd-redis:
```

Then from your application code:

```bash
# Inside the `app` container, dredd is reachable on its compose hostname:
curl -X POST http://dredd:8080/exec -H 'Content-Type: application/json' -d '{
  "language": "python-3.12",
  "source":   "print(\"hello from compose\")",
  "stdins":   [""]
}'
```

**Building rootfs images as a one-shot service** (optional). If you want
the compose stack itself to bootstrap the language rootfs files the first
time it runs, add a setup profile that uses the `dredd-build` image:

```yaml
services:
  dredd-build:
    image: ghcr.io/ondbyte/dredd-build:latest
    profiles: ["setup"]                     # only runs on `compose --profile setup up`
    privileged: true                        # needs loop mount + mkfs.ext4
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /var/lib/dredd:/var/lib/dredd
    environment:
      DREDD_AGENT_BINARY: "/usr/local/bin/dreddagent"
      DREDD_LANGUAGES_FILE: "/var/lib/dredd/languages.json"
      DREDD_ROOTFS_DIR: "/var/lib/dredd/rootfs"
    command: >
      add --image python:3.12 --id python-3.12
          --name Python --version 3.12
          --source main.py --run "python3 /work/main.py"
```

Run it with `docker compose --profile setup run --rm dredd-build` for each
language you want to register. The output (`languages.json` + per-language
`.ext4` files) is written into the shared `/var/lib/dredd` volume that
the long-running `dredd` service mounts.

**Notes on running in cloud Compose hosts**:

- KVM is **not available** in most managed container platforms
  (ECS Fargate, Cloud Run, App Runner). Run dredd on bare-metal or
  KVM-enabled VMs (EC2 metal, GCE nested-virt, Hetzner / dedicated hosts,
  your own Kubernetes node).
- For development without KVM, run the API + queue parts of dredd against
  the `DockerExecutor` (see [Testing](#testing)) — this is a code path
  used by the test suite, not the production binary.

---

## Supported Languages

dredd's language catalogue covers every major language [Judge0](https://judge0.com/) supports, plus multiple compiler/runtime versions per language. Languages are configured at setup time via [`dredd-build`](#one-time-setup) from upstream Docker images.

| Family | Versions | Family | Versions |
|---|---|---|---|
| **C** (GCC) | 7.4, 8.3, 9.2, 10.5, 12.2, 13.2, 14.1 | **Python** | 2.7, 3.8, 3.11, 3.12, 3.13, 3.14 |
| **C** (Clang) | 7.0, 19.1 | **Node.js** | 12, 16, 20, 22 |
| **C++** (GCC) | 7.4, 9.2, 13.2, 14.1 | **TypeScript** | 3.7, 5.0, 5.6 |
| **C++** (Clang) | 7.0 | **Go** | 1.13, 1.18, 1.21, 1.23 |
| **C#** | Mono 6 | **Rust** | 1.40, 1.85 |
| **Java** | OpenJDK 13, JDK 17 | **Kotlin** | 1.3, 2.1 |
| **Scala** | 2.13, 3.4 | **PHP** | 7.4, 8.3 |
| **Ruby** | 3.3 | **Perl** | 5.38 |
| **Lua** | 5.4 | **R** | 4.0, 4.4 |
| **Bash** | 5 | **Swift** | 5.10 |
| **Dart** | stable | **Elixir** | 1.16 |
| **Erlang** | 26 | **Haskell** | GHC 9.6 |
| **OCaml** | 5.1 | **F#** | .NET 8 |
| **Groovy** | 4 (JDK 17) | **Clojure** | Temurin 21 |
| **D** | DMD | **Common Lisp** | SBCL |
| **Pascal** | Free Pascal | **Fortran** | gfortran 13 |
| **Octave** | latest | **Prolog** | SWI |
| **Assembly** | NASM | **COBOL** | GnuCOBOL |
| **FreeBASIC** | 1.10 | **SQLite** | latest |
| **Objective-C** | GNU runtime | **Visual Basic.NET** | Mono |
| **JavaFX** | Liberica 17 | | |

71 (language, version) combinations are exercised by the test matrix — see [Testing](#testing).

---

## Architecture

```
client ─POST /exec─▶ api ──LPUSH──▶ Redis queue ──BRPOP──▶ worker
                                                             │ Acquire
                                                             ▼
                                                          VMPool
                                                             │
                                                             ▼
                                                    Firecracker MicroVM
                                                    └ dreddagent (vsock)
                                                             │
                                                          results
                                                             ▼
client ◀─GET /status── api ◀──HGETALL── Redis hash ◀──HSET── worker
```

- **API layer** (chi + `net/http`): validates requests, enforces a configurable max body size, generates a ULID, enqueues.
- **Redis queue** (`LPUSH/BRPOP` + per-job hash with TTL): cheap, durable, supports horizontal worker scaling.
- **Worker pool**: configurable concurrency; pulls jobs, asks the **Executor** to run them, persists results.
- **Executor seam**: production uses a Firecracker-backed `VMPool`; tests use an in-process `LocalExecutor` or a Docker-backed `DockerExecutor`.
- **Guest agent** (`dreddagent`): a tiny Go init binary that lives inside each rootfs. Listens on vsock, compiles once, runs each stdin sequentially, replies, powers off.
- **Single-retry** on VM-side failure (boot, transport, agent crash) — the "one more to use in queue" guarantee.

### VM pool strategies

Three pool strategies, selectable via `DREDD_POOL_STRATEGY`:

- **`prewarmed_per_lang`** — keep N booted VMs per language. Lowest latency, highest memory.
- **`ondemand_spare`** — boot fresh per request, keep one hot spare per language as a retry buddy. Lower memory, ~boot-latency.
- **`prewarmed_generic`** — pool of generic VMs booted from a single multi-toolchain rootfs. Best when many languages with sparse traffic.

---

## API Reference

### `GET /languages`

Returns the configured language catalogue.

```json
[{"id":"python-3.12","name":"Python","version":"3.12"}, ...]
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

→ `202 Accepted`

```json
{"id":"01JF...ULID"}
```

Returns `413 Request Entity Too Large` if the body exceeds `DREDD_MAX_REQUEST_BYTES` (default 4 MiB).

### `GET /status/{id}`

```json
{
  "id": "01JF...",
  "language": "python-3.12",
  "status": "done",
  "results": [
    {"stdout":"2\n","stderr":"","exit_code":0,"time_ms":31,"memory_kb":8192,"timed_out":false},
    {"stdout":"4\n","stderr":"","exit_code":0,"time_ms":29,"memory_kb":8192,"timed_out":false},
    {"stdout":"6\n","stderr":"","exit_code":0,"time_ms":30,"memory_kb":8192,"timed_out":false}
  ]
}
```

Status values: `queued`, `running`, `done`, `failed`. Terminal status triggers a Redis TTL (`DREDD_RESULT_TTL_SECONDS`, default 300 s) so completed jobs garbage-collect automatically.

### `GET /healthz`

Liveness probe — returns `200 OK` with `{"status":"ok"}`.

---

## One-time setup

Prerequisites: KVM-capable Linux host, a Firecracker `vmlinux` kernel image, Docker (build-time only), Redis, and the `firecracker` binary on `$PATH`.

```bash
go build -o bin/dredd        ./cmd/dredd
go build -o bin/dredd-build  ./cmd/dredd-build
CGO_ENABLED=0 go build -o bin/dreddagent ./guest/dreddagent

sudo mkdir -p /var/lib/dredd/{rootfs,kernel}
sudo cp bin/dreddagent /usr/local/bin/
sudo cp vmlinux /var/lib/dredd/kernel/vmlinux

export DREDD_AGENT_BINARY=/usr/local/bin/dreddagent
export DREDD_LANGUAGES_FILE=/var/lib/dredd/languages.json
export DREDD_ROOTFS_DIR=/var/lib/dredd/rootfs

# Build a language rootfs from any Docker image:
sudo -E ./bin/dredd-build add \
  --image python:3.12 --id python-3.12 --name Python --version 3.12 \
  --source main.py --run "python3 /work/main.py"

sudo -E ./bin/dredd-build add \
  --image gcc:13 --id gcc-13 --name "C (gcc)" --version 13 \
  --source main.c \
  --compile "gcc /work/main.c -O2 -o /work/a.out" \
  --run "/work/a.out"
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DREDD_HTTP_ADDR` | `:8080` | HTTP listen address |
| `DREDD_REDIS_URL` | `redis://localhost:6379/0` | Redis connection |
| `DREDD_LANGUAGES_FILE` | _required_ | Path to languages JSON manifest |
| `DREDD_KERNEL_PATH` | _required_ | Firecracker `vmlinux` |
| `DREDD_ROOTFS_DIR` | `/var/lib/dredd/rootfs` | Per-language ext4 rootfs files |
| `DREDD_POOL_STRATEGY` | `prewarmed_per_lang` | `prewarmed_per_lang` \| `ondemand_spare` \| `prewarmed_generic` |
| `DREDD_POOL_SIZE` | `1` | Idle VMs per language (or total, for generic) |
| `DREDD_BOOT_CONCURRENCY` | `8` | Max parallel Firecracker boots |
| `DREDD_WORKER_CONCURRENCY` | `4` | Parallel job processors |
| `DREDD_RESULT_TTL_SECONDS` | `300` | TTL on completed job hashes |
| `DREDD_VM_VCPUS` | `1` | vCPUs per MicroVM |
| `DREDD_VM_MEM_MB` | `256` | Memory per MicroVM |
| `DREDD_DEFAULT_TIME_LIMIT_MS` | `2000` | Per-test-case time limit |
| `DREDD_DEFAULT_MEMORY_LIMIT_MB` | `256` | Per-test-case memory limit |
| `DREDD_OUTPUT_LIMIT_BYTES` | `1048576` | Cap on stdout/stderr per case |
| `DREDD_MAX_REQUEST_BYTES` | `4194304` | `/exec` body cap (DoS guard) |
| `DREDD_FIRECRACKER_BIN` | `firecracker` | Firecracker binary on `$PATH` |

---

## Embedding dredd in your own Go program

dredd is a library as well as a binary. Import the top-level package and wire your own `App`:

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

Pass `Options.Executor` to plug in a custom isolation backend (gVisor, nsjail, a remote runner, anything that implements `worker.Executor`).

---

## Testing

### Unit + lightweight integration

```bash
go test ./...           # ~10s, no Docker required
go test -race ./...     # race-clean
```

The integration suite uses [`miniredis`](https://github.com/alicebob/miniredis) and an in-process `dreddtest.LocalExecutor` — no host dependencies. Covers `/languages`, `/exec`, `/status`, multi-stdin execution, compile step, retry-on-VM-failure, fail-after-two-failures, TTL expiry.

### Full language matrix (Docker-backed)

```bash
docker login                         # avoids Docker Hub anonymous pull caps
./dreddtest/images/build.sh          # build custom images for NASM, COBOL, etc.
make test-languages                  # runs every language through a real Docker container
```

`TestEndToEnd_AllLanguagesDocker` runs each language inside its own canonical Docker image (e.g. `python:3.12`, `gcc:13`, `rust:1.85`). **No skips** — if Docker isn't reachable or any language fails, the test fails with a clear summary of which IDs broke.

---

## dredd vs Judge0

| Feature | dredd | Judge0 |
|---|---|---|
| Isolation | **Firecracker MicroVM (KVM)** | `isolate` cgroup sandbox |
| Language config | Per-language ext4 rootfs built from any Docker image | Bundled monolithic image |
| Runtime deps | Single Go binary + Redis | Ruby on Rails + PostgreSQL + Redis |
| API style | Async (job id + poll) | Async + optional sync |
| Multi-test-case | Built-in `stdins[]` array | One submission per stdin |
| Library use | Importable Go package | HTTP only |
| Footprint | ~12 MB binary | ~10 GB image |
| License | MIT | GPLv3 |

---

## FAQ

**Is dredd production-ready?**
Beta. Core paths (queue, retry, TTL, pool, race-clean shutdown) are tested end-to-end. Auth, rate limiting, and metrics are intentionally out of scope for v1 — bring your own.

**Why Firecracker instead of Docker?**
Hardware virtualization. Container escapes are a real concern when accepting code from strangers; Firecracker gives you a separate kernel per request with a ~125 ms boot. It's the same trade-off AWS Lambda and Fly.io made.

**Can I run dredd without KVM?**
Not in production — Firecracker needs `/dev/kvm`. But the test suite uses a `DockerExecutor` that runs cases in real Docker containers, so you can validate the API and queue paths on any Linux host with Docker.

**How do I add a new language?**
`./bin/dredd-build add --image <docker-image> --id <language-id> --source <filename> --run <cmd> [--compile <cmd>]`. The catalogue updates automatically.

**Does dredd support code execution APIs for AI agents / LLM tools?**
Yes — that's a primary use case. The async API + Firecracker isolation is well-suited for tool-calling agents that need to evaluate model-generated code without trusting it.

**Is dredd a "remote code execution" service?**
It's a **sandboxed** code execution service: you intentionally expose it to run untrusted code in isolation. Always pair with API authentication and rate limiting at your edge.

**Can I scale dredd horizontally?**
Yes. Redis is the only shared state. Run multiple `dredd` processes against the same Redis and they'll pull jobs cooperatively.

---

## Status

Shipped:

- Core HTTP API, Redis queue, worker pool, three VM-pool strategies, single-retry on VM failure, automatic result TTL.
- 40+ programming languages catalogued and exercised by an end-to-end Docker test matrix.
- Race-clean shutdown verified by `go test -race`.

Planned:

- Production benchmarks at scale.
- Built-in API key authentication (deferred — easy to layer at a reverse proxy today).
- Prometheus / OpenTelemetry metrics.

See [issues](#) for the live roadmap.

---

## Contributing

Patches welcome — please run `go test -race ./...` and `go vet ./...` before opening a PR. For new languages, add an entry to [`dredd_languages_test.go`](dredd_languages_test.go) and verify it passes the Docker matrix.

---

## License

MIT — see [LICENSE](LICENSE).

---

**Keywords**: code execution sandbox, code execution API, Judge0 alternative, Firecracker MicroVM, online judge backend, competitive programming runner, code grader API, untrusted code execution, code execution service Go, self-hosted code runner, Docker code sandbox, multi-language code execution, online IDE backend, code playground API, AI agent code execution, LLM tool sandbox.
