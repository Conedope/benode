package benode

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"
)

// File describes one file inside a torrent's info dict.
type File struct {
	// Path is the slash-joined path of the file. For multi-file torrents
	// this is built from each file dict's "path" list; for single-file
	// torrents it is the info "name" itself.
	Path string
	// Length is the file size in bytes.
	Length uint64
}

// Torrent is the decoded metadata of a .torrent metainfo file (BEP 3).
type Torrent struct {
	// Announce is the primary tracker announce URL (root "announce"), if present.
	Announce string

	// Info is the decoded "info" dictionary of the metainfo.
	Info Dict

	// InfoHash is the SHA1 of the exact serialized bytes of the "info"
	// dictionary as they appear in the source file. This is the value used
	// by BitTorrent peers as the torrent's identifier.
	InfoHash [20]byte

	// Name is the value of info["name"] as a string.
	Name string

	// PieceLength is info["piece length"] in bytes.
	PieceLength int64

	// PieceCount is the number of pieces (= len(Pieces)/20).
	PieceCount int

	// Pieces is the raw concatenated 20-byte SHA1 piece hashes from
	// info["pieces"].
	Pieces []byte

	// Files lists the files described by info. Single-file torrents yield
	// one entry with Path == Name.
	Files []File

	root Dict
}

// ErrNotTorrent is wrapped by errors reporting that data is not valid
// .torrent metainfo (missing or malformed required keys).
var ErrNotTorrent = errors.New("benode: not a valid torrent")

func torrentError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotTorrent, fmt.Sprintf(format, args...))
}

// ParseTorrent decodes data as a .torrent metainfo file. The root must be a
// dict with an "info" key that is itself a dict. The info-hash is the SHA1
// of the exact original bytes of the info dict, so it is stable regardless
// of whether the producer emitted canonically sorted keys.
func ParseTorrent(data []byte) (*Torrent, error) {
	root, _, err := Decode(data)
	if err != nil {
		return nil, err
	}
	rd, ok := root.(Dict)
	if !ok {
		return nil, torrentError("root element is %T, expected a dict", root)
	}

	t := &Torrent{root: rd}

	infoValue, ok := rd["info"]
	if !ok {
		return nil, torrentError("missing required \"info\" key")
	}
	info, ok := infoValue.(Dict)
	if !ok {
		return nil, torrentError("\"info\" must be a dict, got %T", infoValue)
	}
	t.Info = info

	start, end, err := infoSpan(data)
	if err != nil {
		return nil, err
	}
	t.InfoHash = sha1.Sum(data[start:end])

	nameValue, ok := info["name"]
	if !ok {
		return nil, torrentError("info is missing required \"name\"")
	}
	nameBytes, ok := nameValue.(Bytes)
	if !ok {
		return nil, torrentError("info \"name\" must be a byte string, got %T", nameValue)
	}
	t.Name = string(nameBytes)

	if v, ok := info["piece length"]; ok {
		p, ok := v.(Int)
		if !ok {
			return nil, torrentError("info \"piece length\" must be an integer, got %T", v)
		}
		if p <= 0 {
			return nil, torrentError("info \"piece length\" must be positive, got %d", int64(p))
		}
		t.PieceLength = int64(p)
	} else {
		return nil, torrentError("info is missing required \"piece length\"")
	}

	if v, ok := info["pieces"]; ok {
		b, ok := v.(Bytes)
		if !ok {
			return nil, torrentError("info \"pieces\" must be a byte string, got %T", v)
		}
		if len(b)%20 != 0 {
			return nil, torrentError("info \"pieces\" length %d is not a multiple of 20", len(b))
		}
		t.Pieces = append([]byte(nil), b...)
		t.PieceCount = len(b) / 20
	} else {
		return nil, torrentError("info is missing required \"pieces\"")
	}

	if err := t.parseFiles(info); err != nil {
		return nil, err
	}

	if v, ok := rd["announce"]; ok {
		b, ok := v.(Bytes)
		if !ok {
			return nil, torrentError("root \"announce\" must be a byte string, got %T", v)
		}
		t.Announce = string(b)
	}

	return t, nil
}

func (t *Torrent) parseFiles(info Dict) error {
	if v, ok := info["files"]; ok {
		fl, ok := v.(List)
		if !ok {
			return torrentError("info \"files\" must be a list, got %T", v)
		}
		for i, item := range fl {
			fd, ok := item.(Dict)
			if !ok {
				return torrentError("info \"files\" entry %d must be a dict, got %T", i, item)
			}
			var f File
			if lv, ok := fd["length"]; ok {
				li, ok := lv.(Int)
				if !ok {
					return torrentError("file %d \"length\" must be an integer, got %T", i, lv)
				}
				if li < 0 {
					return torrentError("file %d \"length\" must be non-negative, got %d", i, int64(li))
				}
				f.Length = uint64(li)
			} else {
				return torrentError("file %d is missing required \"length\"", i)
			}
			if pv, ok := fd["path"]; ok {
				pl, ok := pv.(List)
				if !ok {
					return torrentError("file %d \"path\" must be a list, got %T", i, pv)
				}
				var parts []string
				for j, partv := range pl {
					pb, ok := partv.(Bytes)
					if !ok {
						return torrentError("file %d \"path\" element %d must be a byte string, got %T", i, j, partv)
					}
					parts = append(parts, string(pb))
				}
				if len(parts) == 0 {
					return torrentError("file %d \"path\" must not be empty", i)
				}
				f.Path = strings.Join(parts, "/")
			} else {
				return torrentError("file %d is missing required \"path\"", i)
			}
			t.Files = append(t.Files, f)
		}
		return nil
	}

	// Single-file torrent: the file is info["name"] with info["length"].
	lv, ok := info["length"]
	if !ok {
		return torrentError("info has neither \"files\" nor \"length\"")
	}
	li, ok := lv.(Int)
	if !ok {
		return torrentError("info \"length\" must be an integer, got %T", lv)
	}
	if li < 0 {
		return torrentError("info \"length\" must be non-negative, got %d", int64(li))
	}
	t.Files = []File{{Path: t.Name, Length: uint64(li)}}
	return nil
}

// infoSpan scans the top level of the metainfo dict and returns the exact
// byte span occupied by the value of the "info" key. Scanning the raw bytes
// (rather than re-encoding the decoded dict) is what makes the info-hash a
// faithful copy of the original serialization, exactly as the BitTorrent
// spec defines it.
func infoSpan(data []byte) (int, int, error) {
	if len(data) == 0 || data[0] != 'd' {
		return 0, 0, torrentError("root element is not a dict")
	}
	pos := 1
	for {
		if pos >= len(data) {
			return 0, 0, torrentError("dict is missing closing 'e'")
		}
		if data[pos] == 'e' {
			return 0, 0, torrentError("missing required \"info\" key")
		}
		kv, kn, err := decodeValue(data, pos)
		if err != nil {
			return 0, 0, err
		}
		key, ok := kv.(Bytes)
		if !ok {
			return 0, 0, torrentError("metainfo key at byte %d is %T, expected a byte string", pos, kv)
		}
		pos = kn
		if string(key) == "info" {
			if pos >= len(data) {
				return 0, 0, torrentError("truncated \"info\" value")
			}
			_, n, err := decodeValue(data, pos)
			if err != nil {
				return 0, 0, err
			}
			return pos, n, nil
		}
		_, n, err := decodeValue(data, pos)
		if err != nil {
			return 0, 0, err
		}
		pos = n
	}
}

// AnnounceURLs returns the announce URL followed by every URL flattened
// from the root "announce-list" (BEP 12) tier list, de-duplicated while
// preserving the first occurrence, in order.
func AnnounceURLs(t *Torrent) []string {
	var urls []string
	seen := make(map[string]bool)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			urls = append(urls, s)
		}
	}
	add(t.Announce)
	if t.root != nil {
		if alv, ok := t.root["announce-list"]; ok {
			if tiers, ok := alv.(List); ok {
				for _, tv := range tiers {
					tier, ok := tv.(List)
					if !ok {
						continue
					}
					for _, uv := range tier {
						if ub, ok := uv.(Bytes); ok {
							add(string(ub))
						}
					}
				}
			}
		}
	}
	return urls
}
