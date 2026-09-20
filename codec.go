// Package benode implements the BitTorrent bencode meta-format: a correct,
// strict, byte-accurate encoder and decoder for integers, byte strings,
// lists and dictionaries, plus a .torrent metadata inspector.
//
// Encoding is always canonical: dictionary keys are serialized sorted
// bytewise ascending, integers in their single canonical form iNeE, and
// byte strings as length-prefixed sequences. Decoding is strict: malformed
// tokens, integer overflow, truncated strings and other corruptions are
// reported as *SyntaxError values carrying the exact byte offset.
package benode

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Value is a bencode value. The only concrete implementations are Int,
// Bytes, List and Dict; the unexported marker method keeps the set closed.
type Value interface {
	isValue()
}

// Int is a bencode integer (arbitrarily small, must fit in an int64).
type Int int64

// Bytes is a bencode byte string. Any byte sequence is allowed, including
// non-UTF-8 and empty strings.
type Bytes []byte

// List is an ordered, heterogenous list of values.
type List []Value

// Dict is an unordered mapping of byte-string keys to values. A decoded
// Dict with duplicate keys keeps the LAST occurrence (later pairs overwrite
// earlier ones), matching the behavior of common BitTorrent clients.
type Dict map[string]Value

func (Int) isValue()   {}
func (Bytes) isValue() {}
func (List) isValue()  {}
func (Dict) isValue()  {}

// ErrSyntax is the sentinel error wrapped by every *SyntaxError. Use
// errors.Is(err, benode.ErrSyntax) to detect malformed input of any kind.
var ErrSyntax = errors.New("benode: syntax error")

// SyntaxError is a decoding failure at a specific byte position.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("benode: syntax error at byte %d: %s", e.Offset, e.Msg)
}

// Unwrap reports that e is a benode.ErrSyntax.
func (e *SyntaxError) Unwrap() error { return ErrSyntax }

// ErrTrailing is returned by Decode when a value decodes cleanly but bytes
// remain after it. The returned value and byte count are still valid.
var ErrTrailing = errors.New("benode: trailing data after value")

// TrailingError wraps ErrTrailing with the byte offset where the trailing
// data begins.
type TrailingError struct {
	Offset int
}

func (e *TrailingError) Error() string {
	return fmt.Sprintf("%s (at byte %d)", ErrTrailing, e.Offset)
}

// Unwrap reports that e is a benode.ErrTrailing.
func (e *TrailingError) Unwrap() error { return ErrTrailing }

func syntaxError(off int, format string, args ...any) error {
	return &SyntaxError{Offset: off, Msg: fmt.Sprintf(format, args...)}
}

// Encode serializes v into canonical bencode. Dictionary keys are emitted
// in bytewise ascending order, so the output is fully deterministic.
func Encode(v Value) []byte {
	var b strings.Builder
	if err := encodeValue(&b, v); err != nil {
		// encodeValue only fails on unrepresentable types; Value is a
		// closed interface so this cannot happen for well-typed input.
		panic(err)
	}
	return []byte(b.String())
}

// EncodeString serializes v into canonical bencode, writing directly to w.
func EncodeString(v Value, w io.Writer) error {
	return encodeValue(w, v)
}

func encodeValue(w io.Writer, v Value) error {
	switch t := v.(type) {
	case Int:
		if _, err := io.WriteString(w, "i"); err != nil {
			return err
		}
		if _, err := io.WriteString(w, strconv.FormatInt(int64(t), 10)); err != nil {
			return err
		}
		_, err := io.WriteString(w, "e")
		return err
	case Bytes:
		if _, err := io.WriteString(w, strconv.Itoa(len(t))); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		_, err := w.Write(t)
		return err
	case List:
		if _, err := io.WriteString(w, "l"); err != nil {
			return err
		}
		for _, item := range t {
			if err := encodeValue(w, item); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "e")
		return err
	case Dict:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i] < keys[j]
		})
		if _, err := io.WriteString(w, "d"); err != nil {
			return err
		}
		for _, k := range keys {
			if err := encodeValue(w, Bytes(k)); err != nil {
				return err
			}
			if err := encodeValue(w, t[k]); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "e")
		return err
	default:
		return fmt.Errorf("benode: cannot encode value of type %T", v)
	}
}

// Decode parses a single bencode value from the start of data. It returns
// the value and the number of bytes consumed. If bytes remain after the
// value, err is ErrTrailing (wrapped in *TrailingError) but the value and
// consumed count are still valid; callers expecting exactly one value
// should treat ErrTrailing as an error. Malformed input yields a
// *SyntaxError carrying the offending byte position.
func Decode(data []byte) (Value, int, error) {
	if len(data) == 0 {
		return nil, 0, syntaxError(0, "empty input")
	}
	v, n, err := decodeValue(data, 0)
	if err != nil {
		return nil, n, err
	}
	if n < len(data) {
		return v, n, &TrailingError{Offset: n}
	}
	return v, n, nil
}

// DecodeAll parses one-or-more adjacent bencode values from data, decoding
// strictly from position 0. It is an error if any leading bytes are not a
// well-formed value (leading garbage is never skipped).
func DecodeAll(data []byte) ([]Value, error) {
	if len(data) == 0 {
		return nil, syntaxError(0, "empty input")
	}
	var out []Value
	pos := 0
	for pos < len(data) {
		v, n, err := decodeValue(data, pos)
		if err != nil {
			return out, err
		}
		out = append(out, v)
		pos = n
	}
	return out, nil
}

// decodeValue decodes exactly one value beginning at data[pos], returning
// the value and the position just past its last byte.
func decodeValue(data []byte, pos int) (Value, int, error) {
	if pos >= len(data) {
		return nil, pos, syntaxError(pos, "unexpected end of input")
	}
	switch data[pos] {
	case 'i':
		return decodeInt(data, pos)
	case 'l':
		return decodeList(data, pos)
	case 'd':
		return decodeDict(data, pos)
	default:
		return decodeString(data, pos)
	}
}

func decodeInt(data []byte, pos int) (Value, int, error) {
	i := pos + 1
	for i < len(data) && data[i] != 'e' {
		i++
	}
	if i >= len(data) {
		return nil, pos, syntaxError(pos, "unterminated integer (missing 'e')")
	}
	digits := string(data[pos+1 : i])
	if digits == "" {
		return nil, pos, syntaxError(pos, "integer with no digits")
	}
	if len(digits) > 1 && digits[0] == '0' {
		return nil, pos, syntaxError(pos, "integer has leading zero (%q)", digits)
	}
	if strings.HasPrefix(digits, "-") {
		body := digits[1:]
		if body == "" {
			return nil, pos, syntaxError(pos, "integer with dangling '-'")
		}
		if body[0] == '0' {
			return nil, pos, syntaxError(pos, "integer has leading zero (%q)", digits)
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return nil, pos, syntaxError(pos, "integer overflows int64 (%q)", digits)
	}
	return Int(n), i + 1, nil
}

func decodeString(data []byte, pos int) (Value, int, error) {
	i := pos
	for i < len(data) && data[i] >= '0' && data[i] <= '9' {
		i++
	}
	if i >= len(data) || data[i] != ':' {
		return nil, pos, syntaxError(pos, "string must start with \"<length>:\"")
	}
	ln, err := strconv.ParseInt(string(data[pos:i]), 10, 32)
	if err != nil || ln < 0 {
		return nil, pos, syntaxError(pos, "invalid string length %q", string(data[pos:i]))
	}
	start := i + 1
	end := start + int(ln)
	if end > len(data) {
		return nil, pos, syntaxError(pos, "string length %d exceeds remaining %d bytes (ends at byte %d > %d)", ln, len(data)-start, end, len(data))
	}
	return Bytes(data[start:end]), end, nil
}

func decodeList(data []byte, pos int) (Value, int, error) {
	var out List
	p := pos + 1
	for {
		if p >= len(data) {
			return nil, pos, syntaxError(pos, "unterminated list (missing 'e')")
		}
		if data[p] == 'e' {
			return out, p + 1, nil
		}
		v, n, err := decodeValue(data, p)
		if err != nil {
			return nil, n, err
		}
		out = append(out, v)
		p = n
	}
}

func decodeDict(data []byte, pos int) (Value, int, error) {
	out := make(Dict)
	p := pos + 1
	for {
		if p >= len(data) {
			return nil, pos, syntaxError(pos, "unterminated dict (missing 'e')")
		}
		if data[p] == 'e' {
			return out, p + 1, nil
		}
		kv, kn, err := decodeValue(data, p)
		if err != nil {
			return nil, kn, err
		}
		key, ok := kv.(Bytes)
		if !ok {
			return nil, p, syntaxError(p, "dict key must be a byte string, got %T", kv)
		}
		vv, vn, err := decodeValue(data, kn)
		if err != nil {
			return nil, vn, err
		}
		// Duplicate keys: the LAST occurrence wins, matching the behavior
		// of widely used BitTorrent clients.
		out[string(key)] = vv
		p = vn
	}
}

// Pretty renders v as a compact, human-readable representation. Dicts and
// lists are flattened back to back (d...e / l...e) like the bencode
// notation itself; byte strings that are short and printable are shown as
// "length:content", anything else as {N bytes}. Integers keep their iNeE form.
func Pretty(v Value) string {
	var sb strings.Builder
	pretty(&sb, v)
	return sb.String()
}

func pretty(sb *strings.Builder, v Value) {
	switch t := v.(type) {
	case Int:
		sb.WriteString(strconv.FormatInt(int64(t), 10))
	case Bytes:
		printable := len(t) <= 32
		for _, b := range t {
			if b < 0x20 || b > 0x7e {
				printable = false
				break
			}
		}
		if printable {
			sb.WriteString(strconv.Itoa(len(t)))
			sb.WriteByte(':')
			sb.WriteString(string(t))
		} else {
			fmt.Fprintf(sb, "{%d bytes}", len(t))
		}
	case List:
		sb.WriteByte('l')
		for _, item := range t {
			pretty(sb, item)
		}
		sb.WriteByte('e')
	case Dict:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		sb.WriteByte('d')
		for _, k := range keys {
			pretty(sb, Bytes(k))
			pretty(sb, t[k])
		}
		sb.WriteByte('e')
	}
}
