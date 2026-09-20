package benode

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"testing"
)

// makePieces returns n deterministic 20-byte piece hashes.
func makePieces(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		sum := sha1.Sum([]byte(fmt.Sprintf("piece-%d", i)))
		b.Write(sum[:])
	}
	return b.Bytes()
}

func sampleInfo(multi bool) (Dict, []byte) {
	pieces := makePieces(4)
	info := Dict{
		"name":         Bytes("benode-sample"),
		"piece length": Int(262144),
		"pieces":       Bytes(pieces),
	}
	if multi {
		info["files"] = List{
			Dict{"length": Int(1024), "path": List{Bytes("README.md")}},
			Dict{"length": Int(2048), "path": List{Bytes("src"), Bytes("main.go")}},
			Dict{"length": Int(0), "path": List{Bytes("empty")}},
		}
	} else {
		info["length"] = Int(3072)
	}
	return info, pieces
}

func TestParseTorrentSingleFile(t *testing.T) {
	info, pieces := sampleInfo(false)
	root := Dict{
		"announce":      Bytes("http://tracker.example:6969/announce"),
		"creation date": Int(1700000000),
		"info":          info,
	}
	data := Encode(root)

	tor, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}

	wantHash := sha1.Sum(Encode(info))
	if tor.InfoHash != wantHash {
		t.Fatalf("InfoHash = %x, want %x", tor.InfoHash, wantHash)
	}
	if tor.Name != "benode-sample" {
		t.Errorf("Name = %q", tor.Name)
	}
	if tor.PieceLength != 262144 {
		t.Errorf("PieceLength = %d", tor.PieceLength)
	}
	if tor.PieceCount != 4 {
		t.Errorf("PieceCount = %d, want 4", tor.PieceCount)
	}
	if !bytes.Equal(tor.Pieces, pieces) {
		t.Errorf("Pieces mismatch")
	}
	if tor.Announce != "http://tracker.example:6969/announce" {
		t.Errorf("Announce = %q", tor.Announce)
	}
	if len(tor.Files) != 1 || tor.Files[0].Path != "benode-sample" || tor.Files[0].Length != 3072 {
		t.Errorf("Files = %#v", tor.Files)
	}
	// Canonical producers round-trip exactly, so re-encoding the info dict
	// reproduces the original bytes/hash.
	if got := Encode(tor.Info); !bytes.Equal(got, Encode(info)) {
		t.Errorf("info re-encode not stable:\n got %q\nwant %q", got, Encode(info))
	}
}

func TestParseTorrentMultiFileWithAnnounceList(t *testing.T) {
	info, _ := sampleInfo(true)
	root := Dict{
		"announce": Bytes("http://primary.example/announce"),
		"announce-list": List{
			List{Bytes("http://primary.example/announce")}, // duplicate of announce
			List{Bytes("udp://tracker2.example:6969/announce"), Bytes("http://tracker3.example/announce")},
		},
		"info": info,
	}
	data := Encode(root)

	tor, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	if tor.PieceCount != 4 {
		t.Errorf("PieceCount = %d", tor.PieceCount)
	}
	want := []File{
		{Path: "README.md", Length: 1024},
		{Path: "src/main.go", Length: 2048},
		{Path: "empty", Length: 0},
	}
	if len(tor.Files) != len(want) {
		t.Fatalf("Files = %#v", tor.Files)
	}
	for i := range want {
		if tor.Files[i] != want[i] {
			t.Errorf("file %d = %#v, want %#v", i, tor.Files[i], want[i])
		}
	}

	urls := AnnounceURLs(tor)
	wantURLs := []string{
		"http://primary.example/announce",
		"udp://tracker2.example:6969/announce",
		"http://tracker3.example/announce",
	}
	if len(urls) != len(wantURLs) {
		t.Fatalf("AnnounceURLs = %#v, want %#v", urls, wantURLs)
	}
	for i := range wantURLs {
		if urls[i] != wantURLs[i] {
			t.Errorf("url %d = %q, want %q", i, urls[i], wantURLs[i])
		}
	}
}

// TestInfoHashUsesRawBytes proves the info-hash hashes the exact original
// bytes of the info dict, even when the producer did not sort keys.
func TestInfoHashUsesRawBytes(t *testing.T) {
	pieces := makePieces(1)
	// Keys deliberately out of canonical order: name last.
	rawInfo := "d12:piece lengthi32768e6:pieces20:" + string(pieces) + "4:name6:sample6:lengthi0ee"
	rawRoot := "d4:info" + rawInfo + "e"

	tor, err := ParseTorrent([]byte(rawRoot))
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha1.Sum([]byte(rawInfo))
	if tor.InfoHash != wantHash {
		t.Fatalf("InfoHash = %x, want %x", tor.InfoHash, wantHash)
	}
	// The decoded/re-encoded dict is canonical and therefore differs from
	// the (unsorted) original — the raw-bytes hash is the correct one.
	if bytes.Equal(Encode(tor.Info), []byte(rawInfo)) {
		t.Fatal("expected canonical re-encode to differ from unsorted original")
	}
	reHash := sha1.Sum(Encode(tor.Info))
	if tor.InfoHash == reHash {
		t.Fatal("raw info-hash should not equal the canonical re-encode hash")
	}
	if tor.Name != "sample" || tor.PieceCount != 1 {
		t.Fatalf("parsed fields wrong: name=%q pieces=%d", tor.Name, tor.PieceCount)
	}
}

func TestParseTorrentErrors(t *testing.T) {
	_, pieces := sampleInfo(false)
	validInfo := func() Dict {
		return Dict{
			"name":         Bytes("x"),
			"piece length": Int(16384),
			"pieces":       Bytes(pieces),
			"length":       Int(10),
		}
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not a dict", Encode(List{Int(1)})},
		{"missing info", Encode(Dict{"announce": Bytes("http://x")})},
		{"info not a dict", Encode(Dict{"info": Int(5)})},
		{"info empty dict", Encode(Dict{"info": Dict{}})},
		{"missing name", Encode(Dict{"info": Dict{
			"piece length": Int(1), "pieces": Bytes(pieces), "length": Int(1)}})},
		{"missing piece length", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "pieces": Bytes(pieces), "length": Int(1)}})},
		{"pieces not multiple of 20", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "piece length": Int(1), "pieces": Bytes("short"), "length": Int(1)}})},
		{"files and no length", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "piece length": Int(1), "pieces": Bytes(pieces)}})},
		{"file missing length", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "piece length": Int(1), "pieces": Bytes(pieces),
			"files": List{Dict{"path": List{Bytes("a")}}}}})},
		{"file missing path", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "piece length": Int(1), "pieces": Bytes(pieces),
			"files": List{Dict{"length": Int(3)}}}})},
		{"negative file length", Encode(Dict{"info": Dict{
			"name": Bytes("x"), "piece length": Int(1), "pieces": Bytes(pieces),
			"files": List{Dict{"length": Int(-1), "path": List{Bytes("a")}}}}})},
		{"trailing garbage", append(Encode(Dict{"info": validInfo()}), 'x')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTorrent(tc.data); err == nil {
				t.Fatalf("ParseTorrent(%q): expected error", tc.data)
			}
		})
	}

	// A well-formed minimal torrent parses.
	if _, err := ParseTorrent(Encode(Dict{"info": validInfo()})); err != nil {
		t.Fatalf("valid torrent failed: %v", err)
	}
}

func TestNotTorrentSentinel(t *testing.T) {
	_, err := ParseTorrent(Encode(Dict{"info": Int(5)}))
	if !errors.Is(err, ErrNotTorrent) {
		t.Fatalf("expected ErrNotTorrent, got %v", err)
	}
}

func TestAnnounceURLsNoDuplicates(t *testing.T) {
	info, _ := sampleInfo(false)
	root := Dict{
		"announce": Bytes("http://a/announce"),
		"info":     info,
	}
	tor, err := ParseTorrent(Encode(root))
	if err != nil {
		t.Fatal(err)
	}
	urls := AnnounceURLs(tor)
	if len(urls) != 1 || urls[0] != "http://a/announce" {
		t.Fatalf("AnnounceURLs = %#v", urls)
	}
}
