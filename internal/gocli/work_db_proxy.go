package gocli

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	localWorkDBProxyDriverName   = "nana-work-db-proxy"
	localWorkDBProxyStoppedState = "stopped"
	localWorkDBProxyActiveState  = "active"
)

type localWorkDBProxySupervisor struct {
	socketPath  string
	runtimePath string
	startedAt   string
	listener    net.Listener
	closeOnce   sync.Once
	wg          sync.WaitGroup
}

type localWorkDBProxyRuntimeState struct {
	ProcessID  int    `json:"process_id"`
	SocketPath string `json:"socket_path,omitempty"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	StoppedAt  string `json:"stopped_at,omitempty"`
}

type localWorkDBProxyRequest struct {
	Operation string                       `json:"operation"`
	Query     string                       `json:"query,omitempty"`
	Args      []localWorkDBProxyNamedValue `json:"args,omitempty"`
	TxOptions *localWorkDBProxyTxOptions   `json:"tx_options,omitempty"`
}

type localWorkDBProxyResponse struct {
	Error        string                    `json:"error,omitempty"`
	LastInsertID int64                     `json:"last_insert_id,omitempty"`
	RowsAffected int64                     `json:"rows_affected,omitempty"`
	Columns      []string                  `json:"columns,omitempty"`
	Rows         [][]localWorkDBProxyValue `json:"rows,omitempty"`
}

type localWorkDBProxyTxOptions struct {
	Isolation int  `json:"isolation,omitempty"`
	ReadOnly  bool `json:"read_only,omitempty"`
}

type localWorkDBProxyNamedValue struct {
	Name    string                `json:"name,omitempty"`
	Ordinal int                   `json:"ordinal,omitempty"`
	Value   localWorkDBProxyValue `json:"value"`
}

type localWorkDBProxyValue struct {
	Type  string  `json:"type"`
	Int   int64   `json:"int,omitempty"`
	Float float64 `json:"float,omitempty"`
	Bool  bool    `json:"bool,omitempty"`
	Text  string  `json:"text,omitempty"`
	Bytes string  `json:"bytes,omitempty"`
}

type localWorkDBProxyDriver struct{}

type localWorkDBProxyConn struct {
	socketPath string
	conn       net.Conn
	enc        *json.Encoder
	dec        *json.Decoder
	mu         sync.Mutex
	closed     bool
}

type localWorkDBProxyStmt struct {
	conn  *localWorkDBProxyConn
	query string
}

type localWorkDBProxyTx struct {
	conn *localWorkDBProxyConn
}

type localWorkDBProxyRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

type localWorkDBProxyResult struct {
	lastInsertID int64
	rowsAffected int64
}

type localWorkDBProxySession struct {
	dbPath string
	db     *sql.DB
	conn   *sql.Conn
	tx     *sql.Tx
}

var (
	startLocalWorkDBProxyActiveMu     sync.RWMutex
	startLocalWorkDBProxyActiveSocket string
	startManagedNanaExecutable        = os.Executable
	startManagedNanaCommandFactory    = func(args ...string) (*exec.Cmd, error) {
		executablePath, err := startManagedNanaExecutable()
		if err != nil {
			return nil, err
		}
		return exec.Command(executablePath, args...), nil
	}
	startManagedNanaStart = func(cmd *exec.Cmd) error {
		return cmd.Start()
	}
)

func init() {
	sql.Register(localWorkDBProxyDriverName, &localWorkDBProxyDriver{})
}

func launchLocalWorkDBProxySupervisor() (*localWorkDBProxySupervisor, error) {
	runtimeDir := filepath.Join(githubNanaHome(), "start", "db-proxy")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, err
	}
	store, err := openLocalWorkDBDirect()
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	socketPath := localWorkDBProxySocketPath()
	if err := prepareLocalWorkDBProxySocketPath(socketPath); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0o600)
	startedAt := time.Now().UTC().Format(time.RFC3339)
	supervisor := &localWorkDBProxySupervisor{
		socketPath:  socketPath,
		runtimePath: filepath.Join(runtimeDir, "runtime.json"),
		startedAt:   startedAt,
		listener:    listener,
	}
	setActiveStartLocalWorkDBProxySocket(socketPath)
	if err := writeGithubJSON(supervisor.runtimePath, localWorkDBProxyRuntimeState{
		ProcessID:  os.Getpid(),
		SocketPath: socketPath,
		Status:     localWorkDBProxyActiveState,
		StartedAt:  startedAt,
	}); err != nil {
		clearActiveStartLocalWorkDBProxySocket(socketPath)
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	supervisor.wg.Add(1)
	go supervisor.serve()
	return supervisor, nil
}

func (s *localWorkDBProxySupervisor) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			handleLocalWorkDBProxyConn(conn)
		}()
	}
}

func (s *localWorkDBProxySupervisor) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		if s.listener != nil {
			closeErr = s.listener.Close()
		}
		s.wg.Wait()
		clearActiveStartLocalWorkDBProxySocket(s.socketPath)
		if err := writeGithubJSON(s.runtimePath, localWorkDBProxyRuntimeState{
			ProcessID:  os.Getpid(),
			SocketPath: s.socketPath,
			Status:     localWorkDBProxyStoppedState,
			StartedAt:  s.startedAt,
			StoppedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil && closeErr == nil {
			closeErr = err
		}
		if strings.TrimSpace(s.socketPath) != "" {
			_ = os.Remove(s.socketPath)
		}
	})
	return closeErr
}

func handleLocalWorkDBProxyConn(raw net.Conn) {
	defer raw.Close()
	session := &localWorkDBProxySession{dbPath: localWorkDBPath()}
	defer session.close()
	decoder := json.NewDecoder(raw)
	encoder := json.NewEncoder(raw)
	for {
		var request localWorkDBProxyRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			_ = encoder.Encode(localWorkDBProxyResponse{Error: err.Error()})
			return
		}
		response := session.handle(request)
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func prepareLocalWorkDBProxySocketPath(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket != 0 {
		conn, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("DB proxy socket already active at %s", socketPath)
		}
	}
	return os.Remove(socketPath)
}

func (s *localWorkDBProxySession) handle(request localWorkDBProxyRequest) localWorkDBProxyResponse {
	if err := s.ensureConn(); err != nil {
		return localWorkDBProxyResponse{Error: err.Error()}
	}
	switch request.Operation {
	case "ping":
		if _, err := s.execContext(`SELECT 1`, nil); err != nil {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		return localWorkDBProxyResponse{}
	case "exec":
		result, err := s.execContext(request.Query, request.Args)
		if err != nil {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		lastInsertID, _ := result.LastInsertId()
		rowsAffected, _ := result.RowsAffected()
		return localWorkDBProxyResponse{
			LastInsertID: lastInsertID,
			RowsAffected: rowsAffected,
		}
	case "query":
		columns, rows, err := s.queryContext(request.Query, request.Args)
		if err != nil {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		return localWorkDBProxyResponse{
			Columns: columns,
			Rows:    rows,
		}
	case "begin":
		if s.tx != nil {
			return localWorkDBProxyResponse{Error: "transaction already active"}
		}
		options := &sql.TxOptions{}
		if request.TxOptions != nil {
			options.Isolation = sql.IsolationLevel(request.TxOptions.Isolation)
			options.ReadOnly = request.TxOptions.ReadOnly
		}
		tx, err := s.conn.BeginTx(context.Background(), options)
		if err != nil {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		s.tx = tx
		return localWorkDBProxyResponse{}
	case "commit":
		if s.tx == nil {
			return localWorkDBProxyResponse{Error: "transaction not active"}
		}
		err := s.tx.Commit()
		s.tx = nil
		if err != nil {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		return localWorkDBProxyResponse{}
	case "rollback":
		if s.tx == nil {
			return localWorkDBProxyResponse{Error: "transaction not active"}
		}
		err := s.tx.Rollback()
		s.tx = nil
		if err != nil && !errors.Is(err, sql.ErrTxDone) {
			return localWorkDBProxyResponse{Error: err.Error()}
		}
		return localWorkDBProxyResponse{}
	case "close":
		s.close()
		return localWorkDBProxyResponse{}
	default:
		return localWorkDBProxyResponse{Error: fmt.Sprintf("unsupported DB proxy operation %q", request.Operation)}
	}
}

func (s *localWorkDBProxySession) ensureConn() error {
	if s.conn != nil {
		return nil
	}
	db, err := openLocalWorkSQLiteDirect(s.dbPath)
	if err != nil {
		return err
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return err
	}
	s.db = db
	s.conn = conn
	return nil
}

func (s *localWorkDBProxySession) close() {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}

func (s *localWorkDBProxySession) execContext(query string, args []localWorkDBProxyNamedValue) (sql.Result, error) {
	values, err := decodeLocalWorkDBProxyArgs(args)
	if err != nil {
		return nil, err
	}
	execer := any(s.conn)
	if s.tx != nil {
		execer = s.tx
	}
	switch target := execer.(type) {
	case *sql.Conn:
		return target.ExecContext(context.Background(), query, values...)
	case *sql.Tx:
		return target.ExecContext(context.Background(), query, values...)
	default:
		return nil, fmt.Errorf("DB proxy exec target unavailable")
	}
}

func (s *localWorkDBProxySession) queryContext(query string, args []localWorkDBProxyNamedValue) ([]string, [][]localWorkDBProxyValue, error) {
	values, err := decodeLocalWorkDBProxyArgs(args)
	if err != nil {
		return nil, nil, err
	}
	queryer := any(s.conn)
	if s.tx != nil {
		queryer = s.tx
	}
	var rows *sql.Rows
	switch target := queryer.(type) {
	case *sql.Conn:
		rows, err = target.QueryContext(context.Background(), query, values...)
	case *sql.Tx:
		rows, err = target.QueryContext(context.Background(), query, values...)
	default:
		return nil, nil, fmt.Errorf("DB proxy query target unavailable")
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := make([][]localWorkDBProxyValue, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for index := range values {
			dest[index] = &values[index]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		encoded := make([]localWorkDBProxyValue, 0, len(values))
		for _, value := range values {
			item, err := encodeLocalWorkDBProxyValue(value)
			if err != nil {
				return nil, nil, err
			}
			encoded = append(encoded, item)
		}
		out = append(out, encoded)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return columns, out, nil
}

func (d *localWorkDBProxyDriver) Open(name string) (driver.Conn, error) {
	socketPath := strings.TrimSpace(name)
	if socketPath == "" {
		return nil, fmt.Errorf("DB proxy socket path is required")
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &localWorkDBProxyConn{
		socketPath: socketPath,
		conn:       conn,
		enc:        json.NewEncoder(conn),
		dec:        json.NewDecoder(conn),
	}, nil
}

func (c *localWorkDBProxyConn) Prepare(query string) (driver.Stmt, error) {
	return &localWorkDBProxyStmt{conn: c, query: query}, nil
}

func (c *localWorkDBProxyConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if err := c.enc.Encode(localWorkDBProxyRequest{Operation: "close"}); err == nil {
		var response localWorkDBProxyResponse
		if decodeErr := c.dec.Decode(&response); decodeErr == nil && strings.TrimSpace(response.Error) != "" {
			_ = c.conn.Close()
			return errors.New(response.Error)
		}
	}
	return c.conn.Close()
}

func (c *localWorkDBProxyConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *localWorkDBProxyConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	_, err := c.call(localWorkDBProxyRequest{
		Operation: "begin",
		TxOptions: &localWorkDBProxyTxOptions{
			Isolation: int(options.Isolation),
			ReadOnly:  options.ReadOnly,
		},
	})
	if err != nil {
		return nil, err
	}
	return &localWorkDBProxyTx{conn: c}, nil
}

func (c *localWorkDBProxyConn) Ping(_ context.Context) error {
	_, err := c.call(localWorkDBProxyRequest{Operation: "ping"})
	return err
}

func (c *localWorkDBProxyConn) CheckNamedValue(value *driver.NamedValue) error {
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return err
	}
	value.Value = converted
	return nil
}

func (c *localWorkDBProxyConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	encoded, err := encodeLocalWorkDBProxyArgs(args)
	if err != nil {
		return nil, err
	}
	response, err := c.call(localWorkDBProxyRequest{
		Operation: "exec",
		Query:     query,
		Args:      encoded,
	})
	if err != nil {
		return nil, err
	}
	return localWorkDBProxyResult{
		lastInsertID: response.LastInsertID,
		rowsAffected: response.RowsAffected,
	}, nil
}

func (c *localWorkDBProxyConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	encoded, err := encodeLocalWorkDBProxyArgs(args)
	if err != nil {
		return nil, err
	}
	response, err := c.call(localWorkDBProxyRequest{
		Operation: "query",
		Query:     query,
		Args:      encoded,
	})
	if err != nil {
		return nil, err
	}
	decodedRows := make([][]driver.Value, 0, len(response.Rows))
	for _, row := range response.Rows {
		decoded := make([]driver.Value, 0, len(row))
		for _, value := range row {
			item, err := decodeLocalWorkDBProxyValue(value)
			if err != nil {
				return nil, err
			}
			decoded = append(decoded, item)
		}
		decodedRows = append(decodedRows, decoded)
	}
	return &localWorkDBProxyRows{
		columns: append([]string{}, response.Columns...),
		rows:    decodedRows,
	}, nil
}

func (c *localWorkDBProxyConn) call(request localWorkDBProxyRequest) (localWorkDBProxyResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return localWorkDBProxyResponse{}, net.ErrClosed
	}
	if err := c.enc.Encode(request); err != nil {
		return localWorkDBProxyResponse{}, err
	}
	var response localWorkDBProxyResponse
	if err := c.dec.Decode(&response); err != nil {
		return localWorkDBProxyResponse{}, err
	}
	if strings.TrimSpace(response.Error) != "" {
		return localWorkDBProxyResponse{}, errors.New(response.Error)
	}
	return response, nil
}

func (s *localWorkDBProxyStmt) Close() error {
	return nil
}

func (s *localWorkDBProxyStmt) NumInput() int {
	return -1
}

func (s *localWorkDBProxyStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, 0, len(args))
	for index, value := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: value})
	}
	return s.conn.ExecContext(context.Background(), s.query, named)
}

func (s *localWorkDBProxyStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, 0, len(args))
	for index, value := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: value})
	}
	return s.conn.QueryContext(context.Background(), s.query, named)
}

func (t *localWorkDBProxyTx) Commit() error {
	_, err := t.conn.call(localWorkDBProxyRequest{Operation: "commit"})
	return err
}

func (t *localWorkDBProxyTx) Rollback() error {
	_, err := t.conn.call(localWorkDBProxyRequest{Operation: "rollback"})
	return err
}

func (r localWorkDBProxyResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r localWorkDBProxyResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

func (r *localWorkDBProxyRows) Columns() []string {
	return append([]string{}, r.columns...)
}

func (r *localWorkDBProxyRows) Close() error {
	r.index = len(r.rows)
	return nil
}

func (r *localWorkDBProxyRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.index]
	r.index++
	for index := range dest {
		if index < len(row) {
			dest[index] = row[index]
		} else {
			dest[index] = nil
		}
	}
	return nil
}

func encodeLocalWorkDBProxyArgs(args []driver.NamedValue) ([]localWorkDBProxyNamedValue, error) {
	out := make([]localWorkDBProxyNamedValue, 0, len(args))
	for _, arg := range args {
		encoded, err := encodeLocalWorkDBProxyValue(arg.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, localWorkDBProxyNamedValue{
			Name:    arg.Name,
			Ordinal: arg.Ordinal,
			Value:   encoded,
		})
	}
	return out, nil
}

func decodeLocalWorkDBProxyArgs(args []localWorkDBProxyNamedValue) ([]any, error) {
	out := make([]any, 0, len(args))
	for _, arg := range args {
		value, err := decodeLocalWorkDBProxyValue(arg.Value)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(arg.Name) != "" {
			out = append(out, sql.Named(arg.Name, value))
			continue
		}
		out = append(out, value)
	}
	return out, nil
}

func encodeLocalWorkDBProxyValue(value any) (localWorkDBProxyValue, error) {
	switch typed := value.(type) {
	case nil:
		return localWorkDBProxyValue{Type: "null"}, nil
	case int64:
		return localWorkDBProxyValue{Type: "int", Int: typed}, nil
	case float64:
		return localWorkDBProxyValue{Type: "float", Float: typed}, nil
	case bool:
		return localWorkDBProxyValue{Type: "bool", Bool: typed}, nil
	case string:
		return localWorkDBProxyValue{Type: "string", Text: typed}, nil
	case []byte:
		return localWorkDBProxyValue{Type: "bytes", Bytes: base64.StdEncoding.EncodeToString(typed)}, nil
	case time.Time:
		return localWorkDBProxyValue{Type: "time", Text: typed.UTC().Format(time.RFC3339Nano)}, nil
	default:
		converted, err := driver.DefaultParameterConverter.ConvertValue(value)
		if err != nil {
			return localWorkDBProxyValue{}, err
		}
		if converted == value {
			return localWorkDBProxyValue{}, fmt.Errorf("unsupported DB proxy value type %T", value)
		}
		return encodeLocalWorkDBProxyValue(converted)
	}
}

func decodeLocalWorkDBProxyValue(value localWorkDBProxyValue) (driver.Value, error) {
	switch value.Type {
	case "", "null":
		return nil, nil
	case "int":
		return value.Int, nil
	case "float":
		return value.Float, nil
	case "bool":
		return value.Bool, nil
	case "string":
		return value.Text, nil
	case "bytes":
		decoded, err := base64.StdEncoding.DecodeString(value.Bytes)
		if err != nil {
			return nil, err
		}
		return decoded, nil
	case "time":
		parsed, err := time.Parse(time.RFC3339Nano, value.Text)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported DB proxy value encoding %q", value.Type)
	}
}

func activeStartLocalWorkDBProxySocket() string {
	startLocalWorkDBProxyActiveMu.RLock()
	defer startLocalWorkDBProxyActiveMu.RUnlock()
	return startLocalWorkDBProxyActiveSocket
}

func setActiveStartLocalWorkDBProxySocket(socketPath string) {
	startLocalWorkDBProxyActiveMu.Lock()
	defer startLocalWorkDBProxyActiveMu.Unlock()
	startLocalWorkDBProxyActiveSocket = strings.TrimSpace(socketPath)
}

func clearActiveStartLocalWorkDBProxySocket(socketPath string) {
	startLocalWorkDBProxyActiveMu.Lock()
	defer startLocalWorkDBProxyActiveMu.Unlock()
	if strings.TrimSpace(startLocalWorkDBProxyActiveSocket) == strings.TrimSpace(socketPath) {
		startLocalWorkDBProxyActiveSocket = ""
	}
}

func localWorkDBProxyRuntimePath() string {
	return filepath.Join(githubNanaHome(), "start", "db-proxy", "runtime.json")
}

func localWorkDBProxySocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("nana-wdb-%s.sock", shortHash(githubNanaHome())))
}

func localWorkDBProxySocketPresent(socketPath string) (bool, error) {
	info, err := os.Lstat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode()&os.ModeSocket != 0, nil
}

func startManagedNanaCommand(args ...string) (*exec.Cmd, error) {
	cmd, err := startManagedNanaCommandFactory(args...)
	if err != nil {
		return nil, err
	}
	// Managed child Nana processes must execute directly instead of routing
	// back through the per-user service socket, or detached/background work
	// can queue behind the command that spawned it.
	cmd.Env = append(cmd.Environ(), nanaServiceInternalEnv+"=1")
	return cmd, nil
}
