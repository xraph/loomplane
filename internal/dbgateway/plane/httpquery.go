package plane

import (
	"context"
	"errors"
	"net/http"
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
	// httpMaxResult is what one request's results may cost, as rowCost and
	// the other charges in httpresponse.go count it: a bound on what they
	// hold in memory and on the encoded response.
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

var errResultTooLarge = gatewayError("54000", msgResultTooLarge)

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
	arrayMode := strings.EqualFold(req.Header.Get("Neon-Array-Mode"), "true")

	// The body is read only once the password checks out, and before a
	// connection is leased, so neither an unauthenticated client nor a slow
	// one holds anything that matters.
	var (
		stmts   []statement
		batch   bool
		results []*queryResult
	)
	readBody := func() (err error) {
		stmts, batch, err = q.readStatements(w, req)
		return err
	}
	err := q.r.withLease(ctx, target.host, target.user, target.password, readBody, func(l *Lease) (LeaseState, error) {
		var (
			state LeaseState
			err   error
		)
		results, state, err = q.run(ctx, l.Conn(), stmts, arrayMode)
		return state, err
	})
	if err != nil {
		q.fail(w, err)
		return
	}
	writeResults(w, batch, results)
}

// run sends every statement in one pipeline, Parse, Bind, Describe and
// Execute each, then Sync, and reads the replies. It returns the client's
// results and the state to release the lease in.
//
// The lease goes back LeaseSession when any statement may have left session
// state, so DISCARD ALL clears it. A transaction still open at the end, from
// a failed batch or a client's own BEGIN, is rolled back. Any I/O failure, a
// result over the cap and a COPY FROM STDIN break the lease.
func (q *httpQuery) run(ctx context.Context, c *BackendConn, stmts []statement, arrayMode bool) ([]*queryResult, LeaseState, error) {
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

	results, status, pgErr, err := q.read(c, stmts, arrayMode)
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
func (q *httpQuery) read(c *BackendConn, stmts []statement, arrayMode bool) (results []*queryResult, status byte, pgErr *pgconn.PgError, err error) {
	results = make([]*queryResult, len(stmts))
	for i := range results {
		results[i] = &queryResult{asArray: arrayMode}
	}
	cur, size := 0, resultCost*len(stmts)
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
				size += fieldCost + jsonStringLen(f.Name)
			}
			if size > q.maxResult {
				return nil, 0, nil, errResultTooLarge
			}
		case *pgproto3.DataRow:
			// Charged before anything is copied, at no less than what the
			// row costs on the wire, in memory and once encoded.
			if size += rowCost(m.Values, res.fields, arrayMode); size > q.maxResult {
				return nil, 0, nil, errResultTooLarge
			}
			row := make([]*string, len(m.Values))
			for i, v := range m.Values {
				if v != nil {
					s := string(v)
					row[i] = &s
				}
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
