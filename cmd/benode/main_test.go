package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Conedope/benode"
)

const fixture = "../../testdata/sample.torrent"

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDecodeCommand(t *testing.T) {
	path := writeTemp(t, "a.ben", "d1:ai42e1:b3:cdde")
	code, out, errOut := exec(t, "decode", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"dict (2 keys) @0", "\"a\" @1", "int 42", "\"b\" @8", "bytes \"cdd\""} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDecodeCommandErrorOffset(t *testing.T) {
	path := writeTemp(t, "bad.ben", "i9223372036854775808e")
	code, _, errOut := exec(t, "decode", path)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "byte 0") {
		t.Errorf("stderr should carry offset: %q", errOut)
	}
}

func TestDecodeCommandTrailing(t *testing.T) {
	path := writeTemp(t, "trail.ben", "i1ex")
	code, out, errOut := exec(t, "decode", path)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout %q)", code, out)
	}
	if !strings.Contains(errOut, "trailing") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestEncodeCommandCanonicalizes(t *testing.T) {
	// Keys deliberately unsorted; enc must emit sorted canonical bytes.
	path := writeTemp(t, "u.ben", "d1:bi2e1:ai1ee")
	code, out, errOut := exec(t, "enc", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "d1:ai1e1:bi2ee" {
		t.Fatalf("enc output = %q", out)
	}
}

func TestEncodeCommandMultipleValues(t *testing.T) {
	path := writeTemp(t, "multi.ben", "i1e3:abc")
	code, out, _ := exec(t, "enc", path)
	if code != 0 || out != "i1e3:abc" {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

func TestTorrentCommand(t *testing.T) {
	code, out, errOut := exec(t, "torrent", fixture)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{
		"info-hash:",
		"benode-sample",
		"piece length:    262144",
		"piece count:     4",
		"announce URLs (3):",
		"README.md",
		"src/main.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("torrent output missing %q:\n%s", want, out)
		}
	}
}

func TestInfoHashCommand(t *testing.T) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	tor, err := benode.ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(tor.InfoHash[:])

	code, out, errOut := exec(t, "info-hash", fixture)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if strings.TrimSpace(out) != want {
		t.Fatalf("info-hash = %q, want %q", strings.TrimSpace(out), want)
	}

	// Independently verify against a hand-computed SHA1 of the info span.
	i := bytes.Index(data, []byte("4:info"))
	if i < 0 {
		t.Fatal("fixture missing info key")
	}
	start := i + len("4:info")
	_, n, err := benode.Decode(data[start:])
	if err != nil && !errors.Is(err, benode.ErrTrailing) {
		t.Fatal(err)
	}
	sum := sha1.Sum(data[start : start+n])
	if hex.EncodeToString(sum[:]) != want {
		t.Fatalf("raw-span SHA1 = %x, want %s", sum, want)
	}
}

func TestTorrentJSON(t *testing.T) {
	code, out, errOut := exec(t, "--json", "torrent", fixture)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	var got struct {
		InfoHash    string `json:"infoHash"`
		Name        string `json:"name"`
		PieceLength int64  `json:"pieceLength"`
		PieceCount  int    `json:"pieceCount"`
		Pieces      []string
		Files       []struct {
			Path   string
			Length uint64
		}
		AnnounceURLs []string `json:"announceUrls"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if got.InfoHash != "b60b0b37956e25d669827e65279bb51ec3e89d96" {
		t.Errorf("infoHash = %q", got.InfoHash)
	}
	if got.Name != "benode-sample" || got.PieceLength != 262144 || got.PieceCount != 4 {
		t.Errorf("basic fields wrong: %+v", got)
	}
	if len(got.Pieces) != 4 || len(got.Pieces[0]) != 40 {
		t.Errorf("pieces = %#v", got.Pieces)
	}
	if len(got.Files) != 2 || got.Files[0].Path != "README.md" || got.Files[0].Length != 1234 {
		t.Errorf("files = %#v", got.Files)
	}
	if len(got.AnnounceURLs) != 3 {
		t.Errorf("announceUrls = %#v", got.AnnounceURLs)
	}
}

func TestDecodeJSON(t *testing.T) {
	path := writeTemp(t, "j.ben", "d1:ai42e1:b2:hie")
	code, out, errOut := exec(t, "--json", "decode", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if got["a"] != "42" { // json.Number round-trips through float64 as "42"
		if n, ok := got["a"].(float64); !ok || n != 42 {
			t.Errorf("a = %#v", got["a"])
		}
	}
	if got["b"] != "hi" {
		t.Errorf("b = %#v", got["b"])
	}
}

func TestUsageExitCodes(t *testing.T) {
	if code, _, _ := exec(t); code != 2 {
		t.Errorf("no args: exit = %d, want 2", code)
	}
	if code, _, _ := exec(t, "decode"); code != 2 {
		t.Errorf("missing file: exit = %d, want 2", code)
	}
	if code, _, _ := exec(t, "bogus", "x"); code != 2 {
		t.Errorf("unknown command: exit = %d, want 2", code)
	}
	if code, _, _ := exec(t, "torrent", "/nonexistent/nope.torrent"); code != 1 {
		t.Errorf("missing file I/O: exit = %d, want 1", code)
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, out, _ := exec(t, "--version")
	if code != 0 || !strings.Contains(out, version) {
		t.Errorf("version: exit=%d out=%q", code, out)
	}
	code, out, _ = exec(t, "--help")
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: exit=%d out=%q", code, out)
	}
}
