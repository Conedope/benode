// Command benode is a bencode codec and .torrent metadata inspector.
//
// It can:
//   - decode a bencode file and print a readable tree with byte offsets,
//   - re-encode a bencode file into its canonical byte form,
//   - inspect a .torrent metainfo file (info-hash, pieces, files, trackers),
//   - print just the info-hash of a .torrent file.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Conedope/benode"
)

const version = "0.1.0"

const usageText = `benode %s — bencode codec + .torrent inspector (offline, stdlib only)

Usage:
  benode [--json] decode FILE     print the bencode value tree of FILE
  benode [--json] enc FILE        re-encode FILE's data as canonical bencode
  benode [--json] torrent FILE    inspect a .torrent metainfo file
  benode [--json] info-hash FILE  print the SHA1 info-hash of a .torrent
  benode --version
  benode --help

Exit codes:
  0  success
  1  I/O or decode error (the offending byte offset is printed)
  2  usage error
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	jsonOut := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "-h", "--help":
			fmt.Fprintf(stdout, usageText, version)
			return 0
		case "--version":
			fmt.Fprintf(stdout, "benode %s\n", version)
			return 0
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintf(stderr, "benode: missing command\n\n%s", usageText)
		return 2
	}
	cmd, args2 := rest[0], rest[1:]

	switch cmd {
	case "decode":
		return runDecode(args2, jsonOut, stdout, stderr)
	case "enc", "encode":
		return runEncode(args2, stdout, stderr)
	case "torrent":
		return runTorrent(args2, jsonOut, stdout, stderr)
	case "info-hash":
		return runInfoHash(args2, jsonOut, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "benode: unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
}

func oneFileArg(args []string, cmd string, stderr io.Writer) (string, bool) {
	if len(args) != 1 {
		fmt.Fprintf(stderr, "benode: %s requires exactly one FILE argument\n\n%s", cmd, usageText)
		return "", false
	}
	return args[0], true
}

func readFile(path string, stderr io.Writer) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "benode: %v\n", err)
		return nil, false
	}
	return data, true
}

func printErr(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "benode: %v\n", err)
}

func printJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(w, "benode: %v\n", err)
		return 1
	}
	return 0
}

// ---- decode ----

func runDecode(args []string, jsonOut bool, stdout, stderr io.Writer) int {
	path, ok := oneFileArg(args, "decode", stderr)
	if !ok {
		return 2
	}
	data, ok := readFile(path, stderr)
	if !ok {
		return 1
	}
	v, n, err := benode.Decode(data)
	if err != nil {
		if errors.Is(err, benode.ErrTrailing) {
			printErr(stderr, err)
			fmt.Fprintf(stderr, "benode: (%d bytes unused after the value)\n", len(data)-n)
		} else {
			printErr(stderr, err)
		}
		return 1
	}
	if jsonOut {
		return printJSON(stdout, valueToJSON(v))
	}
	printTree(stdout, data)
	return 0
}

// decodeTree is a value annotated with its absolute byte offset.
type decodeTree struct {
	kind  string // "int","bytes","list","dict"
	off   int
	text  string
	kids  []*decodeTree
	isKey bool
	key   string
}

// buildTree re-scans the raw bytes from pos, mirroring Decode exactly, so
// every rendered node can carry its true absolute offset.
func buildTree(data []byte, pos int) (*decodeTree, int, error) {
	if pos >= len(data) {
		return nil, pos, &benode.SyntaxError{Offset: pos, Msg: "unexpected end of input"}
	}
	v, n, err := benode.Decode(data[pos:])
	if err != nil && !errors.Is(err, benode.ErrTrailing) {
		if se, ok := err.(*benode.SyntaxError); ok {
			se.Offset += pos
		}
		return nil, pos, err
	}
	switch tv := v.(type) {
	case benode.Int:
		return &decodeTree{kind: "int", off: pos, text: strconv.FormatInt(int64(tv), 10)}, pos + n, nil
	case benode.Bytes:
		return &decodeTree{kind: "bytes", off: pos, text: renderBytes(tv)}, pos + n, nil
	case benode.List:
		nd := &decodeTree{kind: "list", off: pos}
		p := pos + 1
		for p < pos+n-1 {
			child, np, err := buildTree(data, p)
			if err != nil {
				return nil, pos, err
			}
			nd.kids = append(nd.kids, child)
			p = np
		}
		return nd, pos + n, nil
	case benode.Dict:
		nd := &decodeTree{kind: "dict", off: pos}
		p := pos + 1
		for p < pos+n-1 {
			kv, kn, err := benode.Decode(data[p:])
			if err != nil && !errors.Is(err, benode.ErrTrailing) {
				return nil, pos, err
			}
			kb, okName := kv.(benode.Bytes)
			if !okName {
				return nil, pos, &benode.SyntaxError{Offset: p, Msg: "dict key is not a byte string"}
			}
			child, np, err := buildTree(data, p+kn)
			if err != nil {
				return nil, pos, err
			}
			keyNode := &decodeTree{kind: "key", isKey: true, key: string(kb), off: p}
			keyNode.kids = append(keyNode.kids, child)
			nd.kids = append(nd.kids, keyNode)
			p = np
		}
		return nd, pos + n, nil
	default:
		return nil, pos, &benode.SyntaxError{Offset: pos, Msg: "unexpected value"}
	}
}

func renderBytes(b benode.Bytes) string {
	clean := len(b) <= 40 && utf8.Valid(b)
	if clean {
		for _, c := range b {
			if c < 0x20 || c == 0x7f {
				clean = false
				break
			}
		}
	}
	if clean {
		return fmt.Sprintf("%s", strconv.Quote(string(b)))
	}
	return fmt.Sprintf("0x%s", hex.EncodeToString(b))
}

func describe(nd *decodeTree) string {
	switch nd.kind {
	case "int":
		return fmt.Sprintf("int %s @%d", nd.text, nd.off)
	case "bytes":
		return fmt.Sprintf("bytes %s @%d", nd.text, nd.off)
	case "list":
		return fmt.Sprintf("list (%d items) @%d", len(nd.kids), nd.off)
	case "dict":
		return fmt.Sprintf("dict (%d keys) @%d", len(nd.kids), nd.off)
	}
	return ""
}

func printTree(w io.Writer, data []byte) {
	root, _, err := buildTree(data, 0)
	if err != nil {
		fmt.Fprintf(w, "benode: %v\n", err)
		return
	}
	fmt.Fprintf(w, "%s\n", describe(root))
	for i, k := range root.kids {
		emitNode(w, k, "", i == len(root.kids)-1)
	}
}

func emitNode(w io.Writer, nd *decodeTree, prefix string, last bool) {
	branch := "├─ "
	if last {
		branch = "└─ "
	}
	childPref := prefix + "│  "
	if last {
		childPref = prefix + "   "

	}
	if nd.isKey {
		fmt.Fprintf(w, "%s%s\"%s\" @%d\n", prefix, branch, nd.key, nd.off)
		emitValue(w, nd.kids[0], childPref)
		return
	}
	fmt.Fprintf(w, "%s%s%s\n", prefix, branch, describe(nd))
	for i, c := range nd.kids {
		emitNode(w, c, childPref, i == len(nd.kids)-1)
	}
}

func emitValue(w io.Writer, nd *decodeTree, prefix string) {
	fmt.Fprintf(w, "%s→ %s\n", prefix, describe(nd))
	for i, c := range nd.kids {
		emitNode(w, c, prefix+"   ", i == len(nd.kids)-1)
	}
}

// ---- enc ----

func runEncode(args []string, stdout, stderr io.Writer) int {
	path, ok := oneFileArg(args, "enc", stderr)
	if !ok {
		return 2
	}
	data, ok := readFile(path, stderr)
	if !ok {
		return 1
	}
	values, err := benode.DecodeAll(data)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	var sb strings.Builder
	for _, v := range values {
		sb.Write(benode.Encode(v))
	}
	if _, err := io.WriteString(stdout, sb.String()); err != nil {
		printErr(stderr, err)
		return 1
	}
	return 0
}

// ---- torrent ----

func runTorrent(args []string, jsonOut bool, stdout, stderr io.Writer) int {
	path, ok := oneFileArg(args, "torrent", stderr)
	if !ok {
		return 2
	}
	data, ok := readFile(path, stderr)
	if !ok {
		return 1
	}
	t, err := benode.ParseTorrent(data)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	if jsonOut {
		return printJSON(stdout, torrentToJSON(t))
	}
	printTorrent(stdout, t)
	return 0
}

func runInfoHash(args []string, jsonOut bool, stdout, stderr io.Writer) int {
	path, ok := oneFileArg(args, "info-hash", stderr)
	if !ok {
		return 2
	}
	data, ok := readFile(path, stderr)
	if !ok {
		return 1
	}
	t, err := benode.ParseTorrent(data)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	hash := hex.EncodeToString(t.InfoHash[:])
	if jsonOut {
		return printJSON(stdout, map[string]string{"infoHash": hash})
	}
	fmt.Fprintln(stdout, hash)
	return 0
}

func printTorrent(w io.Writer, t *benode.Torrent) {
	fmt.Fprintf(w, "info-hash:       %s\n", hex.EncodeToString(t.InfoHash[:]))
	fmt.Fprintf(w, "name:            %s\n", t.Name)
	fmt.Fprintf(w, "piece length:    %d\n", t.PieceLength)
	fmt.Fprintf(w, "piece count:     %d\n", t.PieceCount)
	fmt.Fprintf(w, "first piece sha1: %s\n", truncPieces(t.Pieces, 3))
	urls := benode.AnnounceURLs(t)
	fmt.Fprintf(w, "announce URLs (%d):\n", len(urls))
	for _, u := range urls {
		fmt.Fprintf(w, "  - %s\n", u)
	}
	fmt.Fprintf(w, "files (%d):\n", len(t.Files))
	for _, f := range t.Files {
		fmt.Fprintf(w, "  %-14d %s\n", f.Length, f.Path)
	}
}

func truncPieces(pieces []byte, show int) string {
	count := len(pieces) / 20
	if count == 0 {
		return "(none)"
	}
	if show > count {
		show = count
	}
	var b strings.Builder
	for i := 0; i < show; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(hex.EncodeToString(pieces[i*20 : (i+1)*20]))
	}
	if show < count {
		fmt.Fprintf(&b, " ... (%d more)", count-show)
	}
	return b.String()
}

func valueToJSON(v benode.Value) any {
	switch t := v.(type) {
	case benode.Int:
		return json.Number(strconv.FormatInt(int64(t), 10))
	case benode.Bytes:
		if utf8.Valid(t) {
			return string(t)
		}
		return map[string]string{"encoding": "base64", "bytes": base64.StdEncoding.EncodeToString(t)}
	case benode.List:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, valueToJSON(item))
		}
		return out
	case benode.Dict:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = valueToJSON(vv)
		}
		return out
	default:
		return nil
	}
}

func torrentToJSON(t *benode.Torrent) map[string]any {
	pieces := make([]string, t.PieceCount)
	for i := range pieces {
		pieces[i] = hex.EncodeToString(t.Pieces[i*20 : i*20+20])
	}
	files := make([]map[string]any, 0, len(t.Files))
	for _, f := range t.Files {
		files = append(files, map[string]any{
			"path":   f.Path,
			"length": json.Number(strconv.FormatUint(f.Length, 10)),
		})
	}
	return map[string]any{
		"infoHash":     hex.EncodeToString(t.InfoHash[:]),
		"name":         t.Name,
		"pieceLength":  json.Number(strconv.FormatInt(t.PieceLength, 10)),
		"pieceCount":   json.Number(strconv.Itoa(t.PieceCount)),
		"pieces":       pieces,
		"files":        files,
		"announce":     t.Announce,
		"announceUrls": benode.AnnounceURLs(t),
	}
}
