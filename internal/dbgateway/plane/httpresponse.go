package plane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// httpAllowHeaders are the request headers a browser may send cross-origin:
// what the pinned Neon driver sends, and Content-Type. Browsers don't accept
// a pattern such as Neon-* here.
var httpAllowHeaders = strings.Join([]string{
	"Authorization", "Content-Type", "Neon-Connection-String", "Neon-Raw-Text-Output", "Neon-Array-Mode",
	"Neon-Batch-Isolation-Level", "Neon-Batch-Read-Only", "Neon-Batch-Deferrable",
}, ", ")

// commandTag splits a CommandComplete tag the way node-postgres does.
var commandTag = regexp.MustCompile(`^([A-Za-z]+)(?: (\d+))?(?: (\d+))?`)

// fieldJSON is one column as node-postgres describes it.
type fieldJSON struct {
	Name             string `json:"name"`
	TableID          uint32 `json:"tableID"`
	ColumnID         uint16 `json:"columnID"`
	DataTypeID       uint32 `json:"dataTypeID"`
	DataTypeSize     int16  `json:"dataTypeSize"`
	DataTypeModifier int32  `json:"dataTypeModifier"`
	Format           string `json:"format"`
}

// queryResult is one statement's result. Values are always text: the driver
// asks for raw text and runs its own type parsers.
type queryResult struct {
	command  *string
	rowCount *int64
	fields   []fieldJSON
	rows     [][]*string // nil for SQL NULL
	asArray  bool
}

// setTag reads the command and row count out of a CommandComplete tag.
func (r *queryResult) setTag(tag string) {
	m := commandTag.FindStringSubmatch(tag)
	if m == nil {
		return
	}
	r.command = &m[1]
	n := m[2]
	if m[3] != "" {
		n = m[3]
	}
	if count, err := strconv.ParseInt(n, 10, 64); err == nil {
		r.rowCount = &count
	}
}

// What a result costs against the request's cap. The charge for each part
// is at least what it holds in memory until the response is written, and at
// least what it adds to the encoded response, which streams out and is
// never held whole. So the cap bounds both.
const (
	// resultCost is one statement's result apart from its fields and rows:
	// the struct, and its command, rowCount and rowAsArray keys.
	resultCost = 128
	// fieldCost is one field apart from its name: the struct, and its keys
	// with the widest values they can have.
	fieldCost = 160
	// rowCost's parts. A DataRow on the wire is 6 bytes, 4 more per value and
	// the value bytes. In memory a row is a slice header, and each value a
	// pointer to a string header and its bytes. Encoded, a row adds two
	// brackets and a comma, and each value a comma, besides the value's own
	// JSON.
	dataRowHeader = 6
	rowOverhead   = 24
	valueWire     = 4
	valueOverhead = 24
)

// rowCost is what one DataRow costs: never less than its wire size plus 24
// bytes a row and 8 a value, so empty and all-NULL rows count. In object
// mode every value also repeats its field's name.
func rowCost(values [][]byte, fields []fieldJSON, arrayMode bool) int {
	cost := dataRowHeader + rowOverhead
	for i, v := range values {
		cost += valueWire + valueOverhead
		if v == nil {
			cost += len("null")
		} else {
			cost += jsonStringLen(v)
		}
		if !arrayMode {
			name := ""
			if i < len(fields) {
				name = fields[i].Name
			}
			// The name, quoted, and the colon, with a byte to spare.
			cost += jsonStringLen([]byte(name)) + 2
		}
	}
	return cost
}

// jsonStringLen is no less than the length of b encoded as a JSON string by
// an Encoder with HTML escaping off, quotes included. It is never less than
// len(b)+2. Quote, backslash, newline, return and tab take two bytes; other
// control bytes may take six (\u00XX); an invalid UTF-8 byte becomes �,
// six; U+2028 and U+2029 become   and  , six for three.
func jsonStringLen(b []byte) int {
	n := 2
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"', c == '\\', c == '\n', c == '\r', c == '\t':
				n += 2
			case c < 0x20:
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += 6
		case r == ' ', r == ' ':
			n += 6
		default:
			n += size
		}
		i += size
	}
	return n
}

// jsonStream writes JSON straight to the response through a small buffer,
// with HTML escaping off. Each value is encoded into scratch, which holds one
// value at a time and is reused, and copied out without the Encoder's
// newline. After the first write error it writes nothing more.
type jsonStream struct {
	w       *bufio.Writer
	scratch bytes.Buffer
	enc     *json.Encoder
	err     error
}

func newJSONStream(w io.Writer) *jsonStream {
	s := &jsonStream{w: bufio.NewWriterSize(w, 32<<10)}
	s.enc = json.NewEncoder(&s.scratch)
	s.enc.SetEscapeHTML(false)
	return s
}

// raw writes JSON text as it is.
func (s *jsonStream) raw(text string) {
	if s.err == nil {
		_, s.err = s.w.WriteString(text)
	}
}

// value writes v's JSON.
func (s *jsonStream) value(v any) {
	if s.err != nil {
		return
	}
	s.scratch.Reset()
	if s.err = s.enc.Encode(v); s.err == nil {
		_, s.err = s.w.Write(bytes.TrimSuffix(s.scratch.Bytes(), []byte("\n")))
	}
}

func (s *jsonStream) flush() {
	if s.err == nil {
		s.err = s.w.Flush()
	}
}

// encode writes the result as the driver reads it. Rows are arrays in array
// mode and otherwise objects keyed by field name, in field order.
func (r *queryResult) encode(s *jsonStream) {
	s.raw(`{"command":`)
	if r.command != nil {
		s.value(*r.command)
	} else {
		s.raw("null")
	}
	s.raw(`,"rowCount":`)
	if r.rowCount != nil {
		s.raw(strconv.FormatInt(*r.rowCount, 10))
	} else {
		s.raw("null")
	}
	s.raw(`,"rowAsArray":` + strconv.FormatBool(r.asArray) + `,"fields":`)
	if r.fields == nil {
		s.raw("[]")
	} else {
		s.value(r.fields)
	}
	s.raw(`,"rows":[`)
	open, end := "{", "}"
	if r.asArray {
		open, end = "[", "]"
	}
	for i, row := range r.rows {
		if i > 0 {
			s.raw(",")
		}
		s.raw(open)
		for j, v := range row {
			if j > 0 {
				s.raw(",")
			}
			if !r.asArray {
				name := ""
				if j < len(r.fields) {
					name = r.fields[j].Name
				}
				s.value(name)
				s.raw(":")
			}
			if v == nil {
				s.raw("null")
			} else {
				s.value(*v)
			}
		}
		s.raw(end)
	}
	s.raw("]}")
}

// writeResults streams a 200 response: one result, or a batch's under
// "results".
func writeResults(w http.ResponseWriter, batch bool, results []*queryResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	s := newJSONStream(w)
	if batch {
		s.raw(`{"results":[`)
	}
	for i, r := range results {
		if i > 0 {
			s.raw(",")
		}
		r.encode(s)
	}
	if batch {
		s.raw("]}")
	}
	s.raw("\n")
	// A client that went away can't be told anything.
	s.flush()
}

// errorBody is the error JSON: the fields the driver copies onto its
// NeonDbError, as node-postgres names them. Positions and line numbers are
// strings, as node-postgres has them.
type errorBody struct {
	Message          string `json:"message"`
	Code             string `json:"code"`
	Severity         string `json:"severity,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Hint             string `json:"hint,omitempty"`
	Position         string `json:"position,omitempty"`
	InternalPosition string `json:"internalPosition,omitempty"`
	InternalQuery    string `json:"internalQuery,omitempty"`
	Where            string `json:"where,omitempty"`
	Schema           string `json:"schema,omitempty"`
	Table            string `json:"table,omitempty"`
	Column           string `json:"column,omitempty"`
	DataType         string `json:"dataType,omitempty"`
	Constraint       string `json:"constraint,omitempty"`
	File             string `json:"file,omitempty"`
	Line             string `json:"line,omitempty"`
	Routine          string `json:"routine,omitempty"`
}

// fail writes err as the error response. Every one is a 400, whatever went
// wrong: the pinned driver parses an error's JSON, and so its code, only on
// a 400 (index.js:1325), and reports any other status as an opaque server
// error.
func (q *httpQuery) fail(w http.ResponseWriter, err error) {
	var (
		ge      *GatewayError
		pgErr   *pgconn.PgError
		tooBig  *http.MaxBytesError
		body    errorBody
		numeric = func(n int32) string {
			if n == 0 {
				return ""
			}
			return strconv.Itoa(int(n))
		}
	)
	switch {
	case errors.As(err, &tooBig):
		body = errorBody{Code: "54000", Message: msgBodyTooLarge}
	case errors.As(err, &ge):
		body = errorBody{Code: ge.Code, Message: ge.Message}
	case errors.As(err, &pgErr):
		body = errorBody{
			Message: pgErr.Message, Code: pgErr.Code, Severity: pgErr.Severity, Detail: pgErr.Detail, Hint: pgErr.Hint,
			Position: numeric(pgErr.Position), InternalPosition: numeric(pgErr.InternalPosition), InternalQuery: pgErr.InternalQuery,
			Where: pgErr.Where, Schema: pgErr.SchemaName, Table: pgErr.TableName, Column: pgErr.ColumnName,
			DataType: pgErr.DataTypeName, Constraint: pgErr.ConstraintName, File: pgErr.File, Line: numeric(pgErr.Line), Routine: pgErr.Routine,
		}
	case errors.Is(err, context.DeadlineExceeded):
		body = errorBody{Code: "57014", Message: msgHTTPTimeout}
	case errors.Is(err, context.Canceled):
		body = errorBody{Code: "57014", Message: msgHTTPCancelled}
	default:
		// Other errors can name the backend's address.
		body = errorBody{Code: "08006", Message: msgNoBackend}
	}
	writeHTTPError(w, http.StatusBadRequest, body)
}

// writeHTTPError writes an error body. The gateway's own errors are ERRORs:
// nothing outlives a request for them to end.
func writeHTTPError(w http.ResponseWriter, status int, body errorBody) {
	if body.Severity == "" {
		body.Severity = "ERROR"
	}
	writeHTTPJSON(w, status, body)
}

func writeHTTPJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) //nolint:errcheck // a client that hung up has nobody to tell
}

// setCORS lets any origin call /sql. The credentials travel in a header,
// never a cookie, so a wildcard origin gives a page nothing it didn't
// already hold. The driver reads no response headers, so none are exposed.
func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", httpAllowHeaders)
}
