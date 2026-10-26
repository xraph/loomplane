package plane

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
)

// The transaction options a batch may ask for, by the values the pinned
// driver sends.
var (
	isolationLevels = map[string]string{
		"ReadUncommitted": "READ UNCOMMITTED",
		"ReadCommitted":   "READ COMMITTED",
		"RepeatableRead":  "REPEATABLE READ",
		"Serializable":    "SERIALIZABLE",
	}
	readOnlyModes   = map[string]string{"true": "READ ONLY", "false": "READ WRITE"}
	deferrableModes = map[string]string{"true": "DEFERRABLE", "false": "NOT DEFERRABLE"}
)

const (
	// maxBatchQueries is how many queries one batch may hold.
	maxBatchQueries = 1000
	// maxRequestParams is how many parameters one request may carry in all,
	// the most one Bind can.
	maxRequestParams = math.MaxUint16
)

// httpRequest is a request body: {query, params} or {queries: [...]}. The
// arrays stay raw until requestLimits walks them, so a body of a million
// tiny parameters is refused at the limit instead of decoded whole.
type httpRequest struct {
	Query   *string         `json:"query"`
	Params  json.RawMessage `json:"params"`
	Queries json.RawMessage `json:"queries"`
}

// batchQuery is one entry of a batch.
type batchQuery struct {
	Query  string          `json:"query"`
	Params json.RawMessage `json:"params"`
}

// requestLimits counts a request's parameters against maxRequestParams.
type requestLimits struct {
	params int
}

func tooMuch(message string) *GatewayError { return gatewayError("54000", message) }

// isNull reports whether a raw member was absent or null.
func isNull(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// eachElement calls fn with each element of the JSON array raw, in order,
// stopping at fn's first error. Absent and null are empty arrays.
func eachElement(raw json.RawMessage, what string, fn func(json.RawMessage) error) error {
	if isNull(raw) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return badRequest(what + " must be an array")
	}
	for dec.More() {
		var elem json.RawMessage
		if err := dec.Decode(&elem); err != nil {
			return badRequest(what + " must be an array")
		}
		if err := fn(elem); err != nil {
			return err
		}
	}
	return nil
}

// textParams turns one query's parameters into text, counting them against
// the request's limit as it goes.
func (lim *requestLimits) textParams(raw json.RawMessage) ([][]byte, error) {
	var params [][]byte
	err := eachElement(raw, "params", func(elem json.RawMessage) error {
		lim.params++
		if lim.params > maxRequestParams {
			return tooMuch("a request takes at most 65535 parameters in all")
		}
		p, err := textParam(elem)
		params = append(params, p)
		return err
	})
	return params, err
}

// statement is one statement a request sends: the client's, or the BEGIN
// and COMMIT around a batch.
type statement struct {
	sql    string
	params [][]byte
	class  Classification
	client bool
}

// connTarget is what the Neon-Connection-String header names. The route
// decides which database the backend connection opens, as it does for a
// WebSocket client's startup database, so database is not used to route.
type connTarget struct {
	host, user, password, database string
}

// readStatements decodes the body into the statements to send. A batch is
// wrapped in BEGIN, with the transaction options from the Neon-Batch-*
// headers, and COMMIT.
func (q *httpQuery) readStatements(w http.ResponseWriter, req *http.Request) ([]statement, bool, error) {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, q.maxBody))
	var body httpRequest
	err := dec.Decode(&body)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, false, err
		}
		return nil, false, badRequest("the request body must be JSON: {\"query\": ..., \"params\": [...]} or {\"queries\": [...]}")
	}

	var lim requestLimits
	switch single, batch := body.Query != nil, !isNull(body.Queries); {
	case single && !batch:
		s, err := lim.clientStatement(*body.Query, body.Params)
		if err != nil {
			return nil, false, err
		}
		return []statement{s}, false, nil
	case batch && !single:
		begin, err := beginStatement(req.Header)
		if err != nil {
			return nil, false, err
		}
		stmts := []statement{{sql: begin}}
		err = eachElement(body.Queries, "queries", func(elem json.RawMessage) error {
			if len(stmts) > maxBatchQueries {
				return tooMuch("a batch takes at most 1000 queries")
			}
			var in batchQuery
			if uerr := json.Unmarshal(elem, &in); uerr != nil {
				return badRequest("each batch entry must be {\"query\": ..., \"params\": [...]}")
			}
			s, serr := lim.clientStatement(in.Query, in.Params)
			stmts = append(stmts, s)
			return serr
		})
		if err != nil {
			return nil, false, err
		}
		return append(stmts, statement{sql: "COMMIT"}), true, nil
	default:
		return nil, false, badRequest("the request body must hold either query or queries")
	}
}

func badRequest(message string) *GatewayError { return gatewayError("08P01", message) }

// clientStatement checks one of the client's queries and turns its
// parameters into text.
func (lim *requestLimits) clientStatement(sql string, raw json.RawMessage) (statement, error) {
	if strings.IndexByte(sql, 0) >= 0 {
		return statement{}, badRequest("a query must not contain a NUL byte")
	}
	params, err := lim.textParams(raw)
	if err != nil {
		return statement{}, err
	}
	return statement{sql: sql, params: params, class: classifyStatement(sql), client: true}, nil
}

// textParam is one parameter in text format: a JSON string's contents, nil
// (SQL NULL) for null, and the JSON text itself for anything else, so a
// number or a boolean goes as written.
func textParam(raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	switch {
	case bytes.Equal(raw, []byte("null")):
		return nil, nil
	case len(raw) > 0 && raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, badRequest("a parameter is not valid JSON")
		}
		// Not nil, even when empty: an empty string is not NULL.
		return append([]byte{}, s...), nil
	default:
		return append([]byte{}, raw...), nil
	}
}

// beginStatement is the BEGIN for a batch. The driver sends each header only
// when the caller set that option, and only these values.
func beginStatement(h http.Header) (string, error) {
	var modes []string
	for _, opt := range []struct {
		header string
		values map[string]string
	}{
		{"Neon-Batch-Isolation-Level", isolationLevels},
		{"Neon-Batch-Read-Only", readOnlyModes},
		{"Neon-Batch-Deferrable", deferrableModes},
	} {
		v := h.Get(opt.header)
		if v == "" {
			continue
		}
		mode, ok := opt.values[v]
		if !ok {
			return "", badRequest("the " + opt.header + " header has a value the gateway does not accept")
		}
		if opt.header == "Neon-Batch-Isolation-Level" {
			mode = "ISOLATION LEVEL " + mode
		}
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return "BEGIN", nil
	}
	return "BEGIN " + strings.Join(modes, ", "), nil
}

// parseConnString reads the Neon-Connection-String header. Query parameters,
// such as the driver's application_name, are ignored. Nothing about a
// header that won't parse is reported: it holds a password.
func parseConnString(s string) (connTarget, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
		return connTarget{}, false
	}
	t := connTarget{host: u.Hostname(), user: u.User.Username(), database: strings.TrimPrefix(u.Path, "/")}
	t.password, _ = u.User.Password()
	if t.host == "" || t.user == "" {
		return connTarget{}, false
	}
	return t, true
}
