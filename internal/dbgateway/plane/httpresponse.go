package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

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

// MarshalJSON writes the result as the driver reads it. Rows are arrays in
// array mode and otherwise objects keyed by field name, in field order.
func (r *queryResult) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"command":`)
	writeJSONValue(&buf, r.command)
	buf.WriteString(`,"rowCount":`)
	writeJSONValue(&buf, r.rowCount)
	buf.WriteString(`,"rowAsArray":`)
	writeJSONValue(&buf, r.asArray)
	buf.WriteString(`,"fields":`)
	fields := r.fields
	if fields == nil {
		fields = []fieldJSON{}
	}
	writeJSONValue(&buf, fields)
	buf.WriteString(`,"rows":[`)
	open, end := byte('{'), byte('}')
	if r.asArray {
		open, end = '[', ']'
	}
	for i, row := range r.rows {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte(open)
		for j, v := range row {
			if j > 0 {
				buf.WriteByte(',')
			}
			if !r.asArray {
				name := ""
				if j < len(r.fields) {
					name = r.fields[j].Name
				}
				writeJSONValue(&buf, name)
				buf.WriteByte(':')
			}
			writeJSONValue(&buf, v)
		}
		buf.WriteByte(end)
	}
	buf.WriteString("]}")
	return buf.Bytes(), nil
}

// writeJSONValue appends v's JSON. The values written here (strings,
// numbers, booleans, fields) always encode.
func writeJSONValue(buf *bytes.Buffer, v any) {
	b, _ := json.Marshal(v)
	buf.Write(b)
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
	_ = json.NewEncoder(w).Encode(v)
}

// setCORS lets any origin call /sql. The credentials travel in a header,
// never a cookie, so a wildcard origin gives a page nothing it didn't
// already hold. The driver reads no response headers, so none are exposed.
func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", httpAllowHeaders)
}
