# goredis-simple

A small Redis-compatible server written in Go using only the standard library. It speaks the Redis wire protocol (RESP), so `redis-cli` and normal Redis client libraries can talk to it. It keeps strings in memory, supports key expiry, and writes every change to a file so data survives a restart.

About 600 lines of code (without tests), one package.

## Run it

```
go build -o goredis-simple .
./goredis-simple                       # listens on :6380, log in ./appendonly.aof
./goredis-simple -addr :7000 -aof ""   # other port, no persistence
```

Then, in another terminal:

```
$ redis-cli -p 6380
127.0.0.1:6380> SET name bob EX 60
OK
127.0.0.1:6380> GET name
"bob"
127.0.0.1:6380> TTL name
(integer) 60
```

Plain `nc localhost 6380` works too: type `PING` and press enter.

## Commands

| Command | Notes |
|---|---|
| `PING [msg]`, `ECHO msg` | |
| `SET key value [EX seconds \| PX milliseconds \| PXAT unix-ms]` | A plain `SET` clears any existing expiry |
| `GET key` | |
| `DEL key [key ...]`, `EXISTS key [key ...]` | Return how many keys were deleted / exist |
| `INCR key` | Missing key counts as 0; keeps the key's expiry |
| `EXPIRE key seconds`, `TTL key` | `TTL` is `-1` with no expiry, `-2` if the key does not exist |
| `PEXPIREAT key unix-ms` | Used by the log; also usable by clients |

Command names are case-insensitive. Errors use the Redis wording, for example `ERR wrong number of arguments for 'get' command`.

## How it works

| File | Job |
|---|---|
| `main.go` | Flags, load the log, accept connections, one goroutine per client |
| `resp.go` | Read commands and write replies in RESP |
| `store.go` | The data: a `map[string]string` and a `map[string]time.Time` of deadlines, behind one `sync.Mutex` |
| `commands.go` | One `switch` on the command name |
| `aof.go` | The append-only file: append, fsync, and replay on startup |

**Locking.** All clients share one store protected by one mutex, and a command holds it from start to finish. That makes every command atomic (`INCR` from 100 clients gives an exact count, which a test checks under `-race`). The cost is that commands run one at a time.

**Expiry** works two ways. A key past its deadline is deleted when a command next looks at it, so a read never returns a stale value. A background goroutine also sweeps every second, so keys nobody reads do not stay in memory forever.

**Persistence.** Every command that changes data is appended to `appendonly.aof`, in the same format clients send, and the file is fsynced before the client gets its reply. On startup the file is run from the top against an empty store.

- Deadlines are logged as absolute times (`SET ... PXAT` and `PEXPIREAT`), not "10 seconds from now", so replaying later does not give a key extra life. `SET` with an expiry is a single log entry, so a crash cannot keep the value and lose its deadline.
- When a key expires, a `DEL` is logged, and replay does not expire anything on its own. That keeps the replayed history identical to what really happened.
- If the server died halfway through writing the last command, that partial command was never acknowledged, so it is ignored and cut off the file. Anything else that does not parse, or that is not a valid command, makes the server refuse to start rather than guess.

## Limitations

- **Every write waits for the disk.** fsync per write is the safest and simplest choice, but it caps write throughput at what the disk can sync. Syncing once per second would be much faster at the price of losing up to a second of writes in a crash.
- **The log only grows.** Nothing rewrites it into a compact form, so it keeps every `INCR` ever made. Startup time grows with it. Compaction (write the current data as a fresh log and swap it in) is the next step.
- **One lock.** Commands run one at a time. Splitting the keys across several locks would let more run in parallel.
- Only strings. No lists, hashes, pub/sub, transactions or authentication.
- Values are held in memory, with no eviction.

## Tests

```
go test -race ./...
```

Covers the parser (including partial and malformed input), each command with a fake clock, restart recovery, a truncated log, concurrent clients over real TCP, and pipelining.
