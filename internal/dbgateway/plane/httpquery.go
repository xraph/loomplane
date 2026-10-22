package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	// httpRequestLimit bounds one HTTP query request, from its first byte to
	// its last.
	httpRequestLimit = 60 * time.Second
	// httpMaxBody is the largest request body the gateway reads.
	httpMaxBody = 10 << 20
	// httpMaxResult is how much row data one request may return.
	httpMaxResult = MaxMessageLen
	// resultSlack is what one backend message may carry on top of its row
	// data: a DataRow spends 2 bytes on its column count and 4 on each
	// column's length, and a row has at most 1664 columns.
	resultSlack = 64 << 10
	// backendMaxBodyLen is pgproto3's default limit on a backend message,
	// Postgres's own PQ_LARGE_MESSAGE_LIMIT. A request lowers the limit while
	// it runs and puts this back.
	backendMaxBodyLen = 0x3fffffff - 1
)

const (
	msgHTTPTimeout     = "the request ran past the gateway's 60 second HTTP limit"
	msgHTTPCancelled   = "the request was cancelled"
	msgResultTooLarge  = "the result is larger than the gateway's HTTP limit; use the WebSocket driver for large results"
	msgBodyTooLarge    = "the request body is larger than the gateway's 10 MiB HTTP limit"
	msgCopyIn          = "COPY FROM STDIN is not supported over HTTP; use the WebSocket driver"
	msgBadConnString   = "the Neon-Connection-String header must be a postgres:// or postgresql:// URL with a user and a host"
	msgUnexpectedReply = "the database sent a reply the gateway did not expect"
)

// httpAllowHeaders are the request headers a browser may send cross-origin:
// what the pinned Neon driver sends, and Content-Type. Browsers don't accept
// a pattern such as Neon-* here.
var httpAllowHeaders = strings.Join([]string{
	"Authorization", "Content-Type", "Neon-Connection-String", "Neon-Raw-Text-Output", "Neon-Array-Mode",
	"Neon-Batch-Isolation-Level", "Neon-Batch-Read-Only", "Neon-Batch-Deferrable",
}, ", ")

var (
	isolationLevels = map[string]string{
		"ReadUncommitted": "READ UNCOMMITTED",
		"ReadCommitted":   "READ COMMITTED",
		"RepeatableRead":  "REPEATABLE READ",
		"Serializable":    "SERIALIZABLE",
	}
	readOnlyModes   = map[string]string{"true": "READ ONLY", "false": "READ WRITE"}
	deferrableModes = map[string]string{"true": "DEFERRABLE", "false": "NOT DEFERRABLE"}

	// commandTag splits a CommandComplete tag the way node-postgres does.
	commandTag = regexp.MustCompile(`^([A-Za-z]+)(?: (\d+))?(?: (\d+))?`)

	errResultTooLarge = gatewayError("54000", msgResultTooLarge)
)

// HTTPQueryHandler serves the Neon serverless driver's HTTP mode at /sql: one
// query, or a batch run in one transaction, per POST, on a connection leased
// from the route's pool for the length of the request. The
// Neon-Connection-String header names the route, role and password, and the
// response is the JSON the pinned driver parses.
func HTTPQueryHandler(r *Router) http.Handler { return newHTTPQuery(r) }

// httpQuery is the /sql handler. Tests shorten its limits.
type httpQuery struct {
	r         *Router
	timeout   time.Duration
	maxBody   int64
	maxResult int
}

func newHTTPQuery(r *Router) *httpQuery {
	return &httpQuery{r: r, timeout: httpRequestLimit, maxBody: httpMaxBody, maxResult: httpMaxResult}
}

// httpRequest is a request body: {query, params} or {queries: [...]}.
type httpRequest struct {
	Query   *string           `json:"query"`
	Params  []json.RawMessage `json:"params"`
	Queries *[]struct {
		Query  string            `json:"query"`
		Params []json.RawMessage `json:"params"`
	} `json:"queries"`
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

func (q *httpQuery) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	setCORS(w.Header())
	if req.URL.Path != "/sql" {
		http.NotFound(w, req)
		return
	}
	switch req.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "POST, OPTIONS")
		writeHTTPError(w, http.StatusMethodNotAllowed, errorBody{Code: "08P01", Message: "the gateway takes POST /sql"})
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), q.timeout)
	defer cancel()
	// A server that doesn't support deadlines (httptest's recorder) returns
	// an error here, and the request is still bounded by ctx once it reaches
	// the backend.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(q.timeout))
	_ = rc.SetWriteDeadline(time.Now().Add(q.timeout))

	target, ok := parseConnString(req.Header.Get("Neon-Connection-String"))
	if !ok {
		writeHTTPError(w, http.StatusBadRequest, errorBody{Code: "08P01", Message: msgBadConnString})
		return
	}
	stmts, batch, err := q.readStatements(w, req)
	if err != nil {
		q.fail(w, err)
		return
	}
	arrayMode := strings.EqualFold(req.Header.Get("Neon-Array-Mode"), "true")

	var results []*queryResult
	err = q.r.WithLease(ctx, target.host, target.user, target.password, func(l *Lease) (LeaseState, error) {
		var (
			state LeaseState
			err   error
		)
		results, state, err = q.run(ctx, l.Conn(), stmts)
		return state, err
	})
	if err != nil {
		q.fail(w, err)
		return
	}
	for _, res := range results {
		res.asArray = arrayMode
	}
	if batch {
		writeHTTPJSON(w, http.StatusOK, struct {
			Results []*queryResult `json:"results"`
		}{results})
		return
	}
	writeHTTPJSON(w, http.StatusOK, results[0])
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

	switch {
	case body.Query != nil && body.Queries == nil:
		s, err := clientStatement(*body.Query, body.Params)
		if err != nil {
			return nil, false, err
		}
		return []statement{s}, false, nil
	case body.Query == nil && body.Queries != nil:
		begin, err := beginStatement(req.Header)
		if err != nil {
			return nil, false, err
		}
		stmts := make([]statement, 0, len(*body.Queries)+2)
		stmts = append(stmts, statement{sql: begin})
		for _, in := range *body.Queries {
			s, err := clientStatement(in.Query, in.Params)
			if err != nil {
				return nil, false, err
			}
			stmts = append(stmts, s)
		}
		return append(stmts, statement{sql: "COMMIT"}), true, nil
	default:
		return nil, false, badRequest("the request body must hold either query or queries")
	}
}

func badRequest(message string) *GatewayError { return gatewayError("08P01", message) }

// clientStatement checks one of the client's queries and turns its
// parameters into text.
func clientStatement(sql string, raw []json.RawMessage) (statement, error) {
	if strings.IndexByte(sql, 0) >= 0 {
		return statement{}, badRequest("a query must not contain a NUL byte")
	}
	if len(raw) > math.MaxUint16 {
		return statement{}, badRequest("a query takes at most 65535 parameters")
	}
	params := make([][]byte, len(raw))
	for i, r := range raw {
		p, err := textParam(r)
		if err != nil {
			return statement{}, err
		}
		params[i] = p
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

// run sends every statement in one pipeline, Parse, Bind, Describe and
// Execute each, then Sync, and reads the replies. It returns the client's
// results and the state to release the lease in.
//
// The lease goes back LeaseSession when any statement may have left session
// state, so DISCARD ALL clears it. A transaction still open at the end, from
// a failed batch or a client's own BEGIN, is rolled back. Any I/O failure, a
// result over the cap and a COPY FROM STDIN break the lease.
func (q *httpQuery) run(ctx context.Context, c *BackendConn, stmts []statement) ([]*queryResult, LeaseState, error) {
	session := false
	for _, s := range stmts {
		switch s.class.Effect {
		case EffectNone, EffectPinStatement, EffectDeallocate:
		default:
			session = true
		}
	}

	// A single DataRow larger than the cap fails as it is read, before its
	// body is allocated.
	c.Frontend.SetMaxBodyLen(q.maxResult + resultSlack)
	defer c.Frontend.SetMaxBodyLen(backendMaxBodyLen)

	// The writes go on their own goroutine: a backend that fills its socket
	// with results stops reading, and a gateway still writing to it would
	// wait for good.
	wrote := make(chan error, 1)
	go func() {
		for _, s := range stmts {
			c.Frontend.Send(&pgproto3.Parse{Query: s.sql})
			c.Frontend.Send(&pgproto3.Bind{Parameters: s.params})
			c.Frontend.Send(&pgproto3.Describe{ObjectType: 'P'})
			c.Frontend.Send(&pgproto3.Execute{})
		}
		c.Frontend.Send(&pgproto3.Sync{})
		err := c.Frontend.Flush()
		if err != nil {
			// The reader would otherwise wait for replies that won't come.
			_ = c.Conn.SetDeadline(time.Unix(1, 0))
		}
		wrote <- err
	}()

	results, status, pgErr, err := q.read(c, stmts)
	if err != nil {
		// The writer may be stuck behind a backend that is still sending.
		_ = c.Conn.SetDeadline(time.Unix(1, 0))
	}
	if werr := <-wrote; err == nil && werr != nil {
		err = werr
	}
	if err != nil {
		c.Broken = true
		var ge *GatewayError
		if errors.As(err, &ge) {
			return nil, LeaseBroken, ge
		}
		if ctx.Err() != nil {
			return nil, LeaseBroken, ctx.Err()
		}
		return nil, LeaseBroken, gatewayError("08006", msgBackendLost)
	}

	state := LeaseClean
	if status != 'I' {
		after, err := roundTrip(ctx, c, "ROLLBACK")
		switch {
		case c.Broken:
			if ctx.Err() != nil {
				return nil, LeaseBroken, ctx.Err()
			}
			return nil, LeaseBroken, gatewayError("08006", msgBackendLost)
		case err != nil || after != 'I':
			state = LeaseDirtyTxn
		}
	}
	if session {
		// DISCARD ALL can't run inside a transaction block, and a rollback
		// that failed leaves session state the pool would keep.
		if state == LeaseDirtyTxn {
			state = LeaseBroken
		} else {
			state = LeaseSession
		}
	}
	if pgErr != nil {
		return nil, state, pgErr
	}
	out := make([]*queryResult, 0, len(results))
	for i, s := range stmts {
		if s.client {
			out = append(out, results[i])
		}
	}
	return out, state, nil
}

// read reads the replies to stmts up to ReadyForQuery. pgErr is the first
// error the backend reported; after one, Postgres skips the rest until Sync.
// err is anything that leaves the connection unusable.
func (q *httpQuery) read(c *BackendConn, stmts []statement) (results []*queryResult, status byte, pgErr *pgconn.PgError, err error) {
	results = make([]*queryResult, len(stmts))
	for i := range results {
		results[i] = &queryResult{}
	}
	cur, size := 0, 0
	for {
		msg, err := c.Frontend.Receive()
		if err != nil {
			var tooBig *pgproto3.ExceededMaxBodyLenErr
			if errors.As(err, &tooBig) {
				return nil, 0, nil, errResultTooLarge
			}
			return nil, 0, nil, err
		}
		switch m := msg.(type) {
		case *pgproto3.ReadyForQuery:
			return results, m.TxStatus, pgErr, nil
		case *pgproto3.ErrorResponse:
			if pgErr == nil {
				pgErr = pgconn.ErrorResponseToPgError(m)
			}
			continue
		case *pgproto3.ParameterStatus:
			if c.Params == nil {
				c.Params = map[string]string{}
			}
			c.Params[m.Name] = m.Value
			continue
		case *pgproto3.ParseComplete, *pgproto3.BindComplete, *pgproto3.NoData,
			*pgproto3.NoticeResponse, *pgproto3.NotificationResponse,
			*pgproto3.CopyOutResponse, *pgproto3.CopyDone:
			continue
		case *pgproto3.CopyInResponse, *pgproto3.CopyBothResponse:
			return nil, 0, nil, gatewayError("0A000", msgCopyIn)
		case *pgproto3.CopyData:
			if size += len(m.Data); size > q.maxResult {
				return nil, 0, nil, errResultTooLarge
			}
			continue
		}

		// The rest belong to the statement whose replies are arriving.
		if cur >= len(stmts) {
			return nil, 0, nil, gatewayError("08006", msgUnexpectedReply)
		}
		res := results[cur]
		switch m := msg.(type) {
		case *pgproto3.RowDescription:
			res.fields = make([]fieldJSON, len(m.Fields))
			for i, f := range m.Fields {
				format := "text"
				if f.Format == 1 {
					format = "binary"
				}
				res.fields[i] = fieldJSON{
					Name: string(f.Name), TableID: f.TableOID, ColumnID: f.TableAttributeNumber,
					DataTypeID: f.DataTypeOID, DataTypeSize: f.DataTypeSize, DataTypeModifier: f.TypeModifier, Format: format,
				}
			}
		case *pgproto3.DataRow:
			row := make([]*string, len(m.Values))
			for i, v := range m.Values {
				if v == nil {
					continue
				}
				if size += len(v); size > q.maxResult {
					return nil, 0, nil, errResultTooLarge
				}
				s := string(v)
				row[i] = &s
			}
			res.rows = append(res.rows, row)
		case *pgproto3.CommandComplete:
			res.setTag(string(m.CommandTag))
			forgetStatements(c, stmts[cur].class)
			cur++
		case *pgproto3.EmptyQueryResponse, *pgproto3.PortalSuspended:
			cur++
		default:
			return nil, 0, nil, gatewayError("08006", msgUnexpectedReply)
		}
	}
}

// forgetStatements drops what a DEALLOCATE that ran removed from the
// connection's record of prepared statements, as a WebSocket session does.
// Prepared statements aren't transactional, so a rollback later doesn't
// bring them back.
func forgetStatements(c *BackendConn, class Classification) {
	if class.Effect != EffectDeallocate {
		return
	}
	if class.Param == "*" {
		c.Statements = map[string]bool{}
		return
	}
	delete(c.Statements, class.Param)
}

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

// gatewayStatus is the HTTP status for each SQLSTATE the gateway itself
// returns. Anything else is a 400, which is also the only status whose JSON
// the driver turns into an error carrying the code.
var gatewayStatus = map[string]int{
	"28P01": http.StatusUnauthorized,
	"08004": http.StatusNotFound,
	"53300": http.StatusServiceUnavailable,
	"57P01": http.StatusServiceUnavailable,
	"08006": http.StatusBadGateway,
}

// fail writes err as the error response.
func (q *httpQuery) fail(w http.ResponseWriter, err error) {
	var (
		ge      *GatewayError
		pgErr   *pgconn.PgError
		tooBig  *http.MaxBytesError
		status  = http.StatusBadRequest
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
		status, body = http.StatusRequestEntityTooLarge, errorBody{Code: "54000", Message: msgBodyTooLarge}
	case errors.As(err, &ge):
		if s, ok := gatewayStatus[ge.Code]; ok {
			status = s
		}
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
		status, body = http.StatusBadGateway, errorBody{Code: "08006", Message: msgNoBackend}
	}
	writeHTTPError(w, status, body)
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
