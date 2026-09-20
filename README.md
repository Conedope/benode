# benode

A correct, strict, dependency-free **bencode** codec and **`.torrent`
metainfo inspector** written in Go. It encodes and decodes the BitTorrent
metaformat (BEP 3), reports decode errors with byte-accurate offsets, and
surfaces the useful parts of a torrent file: info-hash, name, piece length,
piece hashes, file list and tracker URLs.

Standard library only. No network access, no cgo — `CGO_ENABLED=0` builds a
static binary. Requires Go 1.22+.

## The bencode format in one minute

Bencode is a tiny self-describing serialization used by BitTorrent. There
are exactly four types:

| Type | Syntax | Example | Notes |
|------|--------|---------|-------|
| integer | `i<digits>e` | `i-42e` | No leading zeroes, no `-0`; must fit in an `int64`. |
| byte string | `<len>:<bytes>` | `5:hello`, `0:` | Length is decimal, bytes are arbitrary (not necessarily UTF-8). |
| list | `l<values>e` | `l3:abci1ee` | Ordered, heterogeneous. |
| dictionary | `d<key><value>...e` | `d1:a1:be` | Keys are byte strings; **keys are sorted bytewise ascending** in canonical output. |

The encoding is prefix-free and position-driven, which is why a decoder can
report the exact byte where things go wrong. This project treats the format
strictly:

- **Encoding is canonical and deterministic.** Dictionary keys are always
  emitted in bytewise ascending order (Go maps are unordered, so this is
  enforced explicitly). Integers have exactly one representation `i<N>e`.
- **Decoding is strict.** Integers that overflow `int64`, leading zeroes,
  truncated strings, unterminated containers and stray bytes are all
  errors carrying a `*benode.SyntaxError` with a byte `Offset`.
- **Duplicate dictionary keys: the LAST occurrence wins.** Later pairs
  overwrite earlier ones, matching the behavior of common BitTorrent
  clients. Re-encoding then collapses the duplicates deterministically.
- **Empty dicts/lists/strings are legal** (`de`, `le`, `0:`).

### Info-hash stability

A torrent's identity is `SHA1(bencode(info))` over the **exact bytes of the
`info` dictionary as they appear in the file** (BEP 3). `benode` therefore
hashes the original byte span it decoded, not a re-encoding. This stays
correct even for torrents whose producer did not sort keys. For the
well-behaved case — sorted keys and canonical integers, which is what all
common tools emit — decoding followed by `Encode` round-trips to the same
bytes, and the tests assert this on self-produced torrents.

## Install / build

```sh
go build ./cmd/benode      # produces ./benode
# or: go install github.com/Conedope/benode/cmd/benode@latest
```

## Library

```go
import "github.com/Conedope/benode"

v, n, err := benode.Decode(data)      // one value + bytes consumed
vals, err := benode.DecodeAll(data)   // one-or-more adjacent values
out := benode.Encode(v)               // canonical, sorted-key bytes
s := benode.Pretty(v)                 // compact human representation

t, err := benode.ParseTorrent(data)   // .torrent metainfo
```

Core types (each a distinct Go type, all satisfying `Value`):

```go
type Int int64
type Bytes []byte
type List []Value
type Dict map[string]Value
```

`ParseTorrent` returns:

```go
type Torrent struct {
    Announce    string
    Info        Dict
    InfoHash    [20]byte   // SHA1 over the raw info-dict bytes
    Name        string
    PieceLength int64
    PieceCount  int
    Pieces      []byte     // raw concatenated 20-byte SHA1 hashes
    Files       []File     // {Path string; Length uint64}
}

func AnnounceURLs(t *Torrent) []string  // announce + flattened announce-list, de-duplicated
```

### Torrent schema

| Field | Where | Required | Meaning |
|-------|-------|----------|---------|
| `announce` | root | no | Primary tracker URL. |
| `announce-list` | root | no | BEP 12 list of tiers of tracker URLs (flattened by `AnnounceURLs`). |
| `info` | root | **yes** | Info dictionary; its serialized bytes are hashed for the info-hash. |
| `name` | info | **yes** | Suggested name / directory name. |
| `piece length` | info | **yes** | Piece size in bytes (positive). |
| `pieces` | info | **yes** | Concatenated 20-byte SHA1 hashes; length must be a multiple of 20. |
| `length` | info | single-file | File size; used when `files` is absent. |
| `files` | info multi | multi-file | List of `{length, path[]}` entries. |

## CLI usage

```
benode [--json] decode FILE     print the bencode value tree of FILE
benode [--json] enc FILE        re-encode FILE's data as canonical bencode
benode [--json] torrent FILE    inspect a .torrent metainfo file
benode [--json] info-hash FILE  print the SHA1 info-hash of a .torrent
benode --version
benode --help
```

Exit codes: `0` success, `1` I/O or decode error (the offending byte offset
is printed), `2` usage error.

### Generated transcript

The repository ships a small deterministic torrent generated with the
library itself (`examples/gentorrent`). Real output:

```console
$ go run ./examples/gentorrent examples/sample.torrent
wrote examples/sample.torrent (521 bytes)

$ benode info-hash examples/sample.torrent
b60b0b37956e25d669827e65279bb51ec3e89d96

$ benode torrent examples/sample.torrent
info-hash:       b60b0b37956e25d669827e65279bb51ec3e89d96
name:            benode-sample
piece length:    262144
piece count:     4
first piece sha1: 24b6cf31c5f596b25ec46cd2eb45c5c6f0120c01 9f4ca377b9256daf15939930fe538079b644eb2c 412e8e6ed100f083ddd5bdee5b8770d83cb4e5d4 ... (1 more)
announce URLs (3):
  - http://tracker.example:6969/announce
  - udp://tracker2.example:6969/announce
  - https://tracker3.example/announce
files (2):
  1234           README.md
  56789          src/main.go
```

The `decode` command prints the full tree with every node's byte offset:

```console
$ benode decode examples/sample.torrent | head -6
dict (6 keys) @0
├─ "announce" @1
│  → bytes "http://tracker.example:6969/announce" @11
├─ "announce-list" @50
│  → list (2 items) @66
│     ├─ list (1 items) @67
```

`--json` emits a machine-readable form; integer values are emitted as JSON
numbers, and byte strings that are not valid UTF-8 as
`{"encoding":"base64","bytes":"..."}`.

## Tests

```sh
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
```

The suite covers encode/decode goldens (`i42e`, `3:abc`, `l3:abci-1ee`,
`d1:ai42e1:b3:cdde`, `de`, `le`, `0:`), key sorting, position-returning
decodes, trailing-byte detection, integer overflow, string-length overrun,
last-wins duplicate keys, a round-trip corpus (unicode, binary keys,
4-level nesting, `math.MinInt64`/`MaxInt64`), torrent parsing (single- and
multi-file, announce-list, raw-byte info-hash), and the CLI's commands,
JSON output and exit codes.

## License

MIT © 2026 Conedope. See [LICENSE](LICENSE).
