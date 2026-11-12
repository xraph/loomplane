package catalogconnect

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
)

const (
	falkorReplyBytes  = 64 * 1024
	falkorBulkBytes   = 8 * 1024
	falkorLineBytes   = 128
	falkorErrorBytes  = 1024
	falkorArrayItems  = 256
	falkorReplyDepth  = 5
	falkorReplyValues = 2048
	falkorWriteBytes  = 4608
)

var (
	errFalkorResponse = errors.New("the FalkorDB response could not be verified")
	errFalkorProduct  = errors.New("the endpoint did not declare FalkorDB 4.2.2")
	errFalkorAuth     = errors.New("FalkorDB rejected authentication")
	errFalkorPerm     = errors.New("FalkorDB denied the command")
	errFalkorCommand  = errors.New("FalkorDB refused the command")
)

type falkorValue struct {
	kind    byte
	text    string
	number  int64
	members []falkorValue
}

type falkorRESP struct {
	wire   *io.LimitedReader
	reader *bufio.Reader
	values int
}

func newFalkorRESP(reader io.Reader) *falkorRESP {
	wire := &io.LimitedReader{R: reader, N: falkorReplyBytes}
	return &falkorRESP{wire: wire, reader: bufio.NewReader(wire), values: falkorReplyValues}
}

func (r *falkorRESP) remaining() int {
	return int(r.wire.N) + r.reader.Buffered()
}

func (r *falkorRESP) reply() (falkorValue, error) {
	value, err := r.value(1)
	if r.reader.Buffered() > 0 {
		return falkorValue{}, errFalkorResponse
	}
	return value, err
}

func (r *falkorRESP) value(depth int) (falkorValue, error) {
	if depth > falkorReplyDepth || r.values == 0 || r.remaining() == 0 {
		return falkorValue{}, errFalkorResponse
	}
	r.values--
	kind, err := r.reader.ReadByte()
	if err != nil {
		if depth > 1 && errors.Is(err, io.EOF) {
			return falkorValue{}, errFalkorResponse
		}
		return falkorValue{}, err
	}
	switch kind {
	case '-':
		if depth != 1 {
			return falkorValue{}, errFalkorResponse
		}
		line, lineErr := r.line(falkorErrorBytes)
		if lineErr != nil {
			return falkorValue{}, lineErr
		}
		code, _, _ := bytes.Cut(line, []byte(" "))
		switch string(code) {
		case "WRONGPASS", "NOAUTH":
			return falkorValue{}, errFalkorAuth
		case "NOPERM":
			return falkorValue{}, errFalkorPerm
		default:
			return falkorValue{}, errFalkorCommand
		}
	case ':', '$', '*':
	default:
		return falkorValue{}, errFalkorResponse
	}

	line, err := r.line(falkorLineBytes)
	if err != nil {
		return falkorValue{}, err
	}
	number, err := strconv.ParseInt(string(line), 10, 64)
	if err != nil || string(line) != strconv.FormatInt(number, 10) {
		return falkorValue{}, errFalkorResponse
	}
	switch kind {
	case ':':
		return falkorValue{kind: kind, number: number}, nil
	case '$':
		return r.bulk(number)
	default:
		return r.array(number, depth)
	}
}

func (r *falkorRESP) line(limit int) ([]byte, error) {
	line, err := r.reader.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull), errors.Is(err, io.EOF):
		return nil, errFalkorResponse
	case err != nil:
		return nil, err
	case len(line) < 2 || len(line)-2 > limit || line[len(line)-2] != '\r':
		return nil, errFalkorResponse
	}
	line = line[:len(line)-2]
	if bytes.ContainsRune(line, '\r') {
		return nil, errFalkorResponse
	}
	return line, nil
}

func (r *falkorRESP) bulk(size int64) (falkorValue, error) {
	if size < 0 || size > falkorBulkBytes || size+2 > int64(r.remaining()) {
		return falkorValue{}, errFalkorResponse
	}
	data := make([]byte, int(size)+2)
	if _, err := io.ReadFull(r.reader, data); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return falkorValue{}, errFalkorResponse
		}
		return falkorValue{}, err
	}
	if data[size] != '\r' || data[size+1] != '\n' {
		return falkorValue{}, errFalkorResponse
	}
	return falkorValue{kind: '$', text: string(data[:size])}, nil
}

func (r *falkorRESP) array(count int64, depth int) (falkorValue, error) {
	// The shortest supported child frame takes four bytes.
	if count < 0 || count > falkorArrayItems || count > int64(r.values) || count*4 > int64(r.remaining()) || count > 0 && depth == falkorReplyDepth {
		return falkorValue{}, errFalkorResponse
	}
	members := make([]falkorValue, int(count))
	for i := range members {
		item, err := r.value(depth + 1)
		if err != nil {
			return falkorValue{}, err
		}
		members[i] = item
	}
	return falkorValue{kind: '*', members: members}, nil
}

func writeFalkorCommand(w io.Writer, args ...string) error {
	if len(args) == 0 || len(args) > 5 {
		return io.ErrShortWrite
	}
	size := 4
	for _, arg := range args {
		if len(arg) > 4096 {
			return io.ErrShortWrite
		}
		size += len(arg) + 9
	}
	if size > falkorWriteBytes {
		return io.ErrShortWrite
	}
	frame := make([]byte, 0, size)
	frame = append(frame, '*')
	frame = strconv.AppendInt(frame, int64(len(args)), 10)
	frame = append(frame, '\r', '\n')
	for _, arg := range args {
		frame = append(frame, '$')
		frame = strconv.AppendInt(frame, int64(len(arg)), 10)
		frame = append(frame, '\r', '\n')
		frame = append(frame, arg...)
		frame = append(frame, '\r', '\n')
	}
	for len(frame) > 0 {
		written, err := w.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func falkorPairs(value falkorValue, count int) map[string]falkorValue {
	if value.kind != '*' || len(value.members) != count*2 {
		return nil
	}
	fields := make(map[string]falkorValue, count)
	for i := 0; i < len(value.members); i += 2 {
		key := value.members[i]
		if _, exists := fields[key.text]; exists || key.kind != '$' {
			return nil
		}
		fields[key.text] = value.members[i+1]
	}
	return fields
}

func (v falkorValue) bulkIs(text string) bool {
	return v.kind == '$' && v.text == text
}

func validateFalkorHello(value falkorValue) error {
	fields := falkorPairs(value, 7)
	coreVersion, proto, id := fields["version"], fields["proto"], fields["id"]
	role, modules := fields["role"], fields["modules"]
	if !fields["server"].bulkIs("redis") || coreVersion.kind != '$' || coreVersion.text == "" || len(coreVersion.text) > 32 || version(coreVersion.text) != coreVersion.text ||
		proto.kind != ':' || proto.number != 2 || id.kind != ':' || id.number < 1 || !fields["mode"].bulkIs("standalone") ||
		!role.bulkIs("master") && !role.bulkIs("replica") || modules.kind != '*' || len(modules.members) > 32 {
		return errFalkorResponse
	}
	seen := make(map[string]bool, len(modules.members))
	var graphVersion int64
	for _, module := range modules.members {
		entry := falkorPairs(module, 4)
		name, ver, args := entry["name"], entry["ver"], entry["args"]
		if name.kind != '$' || len(name.text) == 0 || len(name.text) > 128 || seen[name.text] ||
			ver.kind != ':' || entry["path"].kind != '$' || args.kind != '*' || len(args.members) > 64 {
			return errFalkorResponse
		}
		seen[name.text] = true
		for _, arg := range args.members {
			if arg.kind != '$' {
				return errFalkorResponse
			}
		}
		if name.text == "graph" {
			graphVersion = ver.number
		}
	}
	if graphVersion != 40202 {
		return errFalkorProduct
	}
	return nil
}

func validFalkorConfig(value falkorValue) bool {
	return value.kind == '*' && len(value.members) == 2 && value.members[0].bulkIs("RESULTSET_SIZE") && value.members[1].kind == ':'
}
