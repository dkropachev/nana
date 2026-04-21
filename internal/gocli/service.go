package gocli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nanaServiceInternalEnv           = "NANA_SERVICE_INTERNAL"
	nanaServiceRuntimeDirOverrideEnv = "NANA_SERVICE_RUNTIME_DIR"
	nanaServiceSocketName            = "service.sock"
	nanaServiceMetadataName          = "service.json"
	nanaServiceStatusActive          = "active"
	nanaServiceStatusStopped         = "stopped"
	nanaServiceRequestTypeCommand    = "command"
	nanaServiceRequestTypeOwnerInfo  = "owner-info"
	nanaServiceEventTypeStdout       = "stdout"
	nanaServiceEventTypeStderr       = "stderr"
	nanaServiceEventTypeDone         = "done"
	nanaServiceEventTypeOwnerInfo    = "owner-info"
)

type nanaServiceSupervisor struct {
	socketPath   string
	metadataPath string
	metadata     nanaServiceMetadata
	listener     net.Listener
	closeOnce    sync.Once
	wg           sync.WaitGroup
	execMu       sync.Mutex
}

type nanaServiceMetadata struct {
	InstanceID string `json:"instance_id"`
	ProcessID  int    `json:"process_id"`
	UID        string `json:"uid"`
	Status     string `json:"status"`
	SocketPath string `json:"socket_path"`
	StartedAt  string `json:"started_at"`
	StoppedAt  string `json:"stopped_at,omitempty"`
}

type nanaServiceRequest struct {
	Type string   `json:"type"`
	Argv []string `json:"argv,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`
}

type nanaServiceEvent struct {
	Type     string               `json:"type"`
	Data     string               `json:"data,omitempty"`
	ExitCode int                  `json:"exit_code,omitempty"`
	Error    string               `json:"error,omitempty"`
	Metadata *nanaServiceMetadata `json:"metadata,omitempty"`
}

type nanaServiceExecContext struct {
	cwd    string
	stdout io.Writer
	stderr io.Writer
}

var (
	nanaServiceWorkStartHandler         = startWorkWithIO
	nanaServiceWorkResumeHandler        = resumeWorkWithIO
	nanaServiceWorkResolveHandler       = resolveWorkWithIO
	nanaServiceWorkStatusHandler        = workStatusWithIO
	nanaServiceWorkLogsHandler          = workLogsWithIO
	nanaServiceWorkRetrospectiveHandler = workRetrospectiveWithIO
	nanaServiceWorkExplainHandler       = githubWorkExplainWithIO
	nanaServiceWorkVerifyRefreshHandler = workVerifyRefreshWithIO
	nanaServiceWorkSyncHandler          = workSyncWithIO
	nanaServiceWorkLaneExecHandler      = workLaneExecWithIO
	nanaServiceRepoHandler              = repoWithIO
	nanaServiceCleanupHandler           = cleanupWithIO
	nanaServiceGithubIssueHandler       = githubIssueWithIO
	nanaServiceGithubReviewHandler      = githubReviewWithIO
	nanaServiceGithubReviewRulesHandler = githubReviewRulesWithIO
)

func launchNanaServiceSupervisor() (*nanaServiceSupervisor, error) {
	runtimeDir := nanaServiceRuntimeDir()
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, err
	}
	socketPath := nanaServiceSocketPath()
	metadataPath := nanaServiceMetadataPath()
	if err := prepareNanaServiceSocketPath(socketPath, metadataPath); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0o600)
	now := time.Now().UTC().Format(time.RFC3339)
	metadata := nanaServiceMetadata{
		InstanceID: strconv.FormatInt(time.Now().UnixNano(), 10),
		ProcessID:  os.Getpid(),
		UID:        nanaServiceUID(),
		Status:     nanaServiceStatusActive,
		SocketPath: socketPath,
		StartedAt:  now,
	}
	supervisor := &nanaServiceSupervisor{
		socketPath:   socketPath,
		metadataPath: metadataPath,
		metadata:     metadata,
		listener:     listener,
	}
	if err := writeGithubJSON(metadataPath, metadata); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	supervisor.wg.Add(1)
	go supervisor.serve()
	return supervisor, nil
}

func (s *nanaServiceSupervisor) serve() {
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
			s.handleConn(conn)
		}()
	}
}

func (s *nanaServiceSupervisor) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		if s.listener != nil {
			closeErr = s.listener.Close()
		}
		s.wg.Wait()
		stopped := s.metadata
		stopped.Status = nanaServiceStatusStopped
		stopped.StoppedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeGithubJSON(s.metadataPath, stopped); err != nil && closeErr == nil {
			closeErr = err
		}
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (s *nanaServiceSupervisor) handleConn(raw net.Conn) {
	defer raw.Close()
	decoder := json.NewDecoder(raw)
	var request nanaServiceRequest
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(raw).Encode(nanaServiceEvent{Type: nanaServiceEventTypeDone, ExitCode: 1, Error: err.Error()})
		return
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()

	encoder := json.NewEncoder(raw)
	switch request.Type {
	case nanaServiceRequestTypeOwnerInfo:
		_ = encoder.Encode(nanaServiceEvent{Type: nanaServiceEventTypeOwnerInfo, Metadata: &s.metadata})
		return
	case nanaServiceRequestTypeCommand:
		if err := s.runCommand(encoder, request); err != nil {
			_ = encoder.Encode(nanaServiceEvent{Type: nanaServiceEventTypeDone, ExitCode: 1, Error: err.Error()})
		}
		return
	default:
		_ = encoder.Encode(nanaServiceEvent{Type: nanaServiceEventTypeDone, ExitCode: 1, Error: fmt.Sprintf("unknown request type %q", request.Type)})
	}
}

func (s *nanaServiceSupervisor) runCommand(encoder *json.Encoder, request nanaServiceRequest) error {
	if len(request.Argv) == 0 {
		return errors.New("service command request requires argv")
	}
	if handler, ok := nanaServiceCommandHandler(request.Argv); ok {
		return s.runInProcessCommand(encoder, request, handler)
	}
	return fmt.Errorf("command %q is not service-owned", request.Argv[0])
}

func (s *nanaServiceSupervisor) runInProcessCommand(encoder *json.Encoder, request nanaServiceRequest, handler func(nanaServiceExecContext, []string) error) error {
	writeMu := sync.Mutex{}
	ctx := nanaServiceExecContext{
		cwd: request.Cwd,
		stdout: &nanaServiceEventWriter{
			mu:        &writeMu,
			encoder:   encoder,
			eventType: nanaServiceEventTypeStdout,
		},
		stderr: &nanaServiceEventWriter{
			mu:        &writeMu,
			encoder:   encoder,
			eventType: nanaServiceEventTypeStderr,
		},
	}
	runErr := handler(ctx, request.Argv[1:])
	exitCode := 0
	doneErr := ""
	if runErr != nil {
		exitCode = 1
		doneErr = runErr.Error()
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	return encoder.Encode(nanaServiceEvent{Type: nanaServiceEventTypeDone, ExitCode: exitCode, Error: doneErr})
}

type nanaServiceEventWriter struct {
	mu        *sync.Mutex
	encoder   *json.Encoder
	eventType string
}

func (w *nanaServiceEventWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.encoder.Encode(nanaServiceEvent{Type: w.eventType, Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func MaybeRunNanaServiceCommand(command string, cwd string, argv []string, stdout io.Writer, stderr io.Writer) (bool, int, error) {
	if isNanaServiceInternalProcess() {
		return false, 0, nil
	}
	_ = command
	if _, ok := nanaServiceCommandHandler(argv); !ok {
		return false, 0, nil
	}
	exitCode, err := runNanaServiceClient(cwd, argv, stdout, stderr)
	return true, exitCode, err
}

func runNanaServiceClient(cwd string, argv []string, stdout io.Writer, stderr io.Writer) (int, error) {
	conn, err := net.Dial("unix", nanaServiceSocketPath())
	if err != nil {
		return 0, nanaServiceUnavailableError()
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(nanaServiceRequest{
		Type: nanaServiceRequestTypeCommand,
		Argv: append([]string{}, argv...),
		Cwd:  cwd,
	}); err != nil {
		return 0, err
	}
	decoder := json.NewDecoder(bufio.NewReader(conn))
	for {
		var event nanaServiceEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return 1, nil
			}
			return 0, err
		}
		switch event.Type {
		case nanaServiceEventTypeStdout:
			if _, err := io.WriteString(stdout, event.Data); err != nil {
				return 0, err
			}
		case nanaServiceEventTypeStderr:
			if _, err := io.WriteString(stderr, event.Data); err != nil {
				return 0, err
			}
		case nanaServiceEventTypeDone:
			if strings.TrimSpace(event.Error) != "" {
				return event.ExitCode, errors.New(event.Error)
			}
			return event.ExitCode, nil
		default:
			return 0, fmt.Errorf("unknown service event type %q", event.Type)
		}
	}
}

func nanaServiceCommandHandler(argv []string) (func(nanaServiceExecContext, []string) error, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	for _, arg := range argv {
		if isHelpToken(arg) {
			return nil, false
		}
	}
	switch argv[0] {
	case "status":
		return func(ctx nanaServiceExecContext, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("status does not accept positional arguments")
			}
			return statusWithIO(ctx.cwd, ctx.stdout, ctx.stderr)
		}, true
	case "next":
		return func(ctx nanaServiceExecContext, args []string) error {
			return nextWithIO(ctx.cwd, args, ctx.stdout)
		}, true
	case "usage":
		return func(ctx nanaServiceExecContext, args []string) error {
			return usageWithIO(ctx.cwd, args, ctx.stdout)
		}, true
	case "artifacts":
		return func(ctx nanaServiceExecContext, args []string) error {
			return artifactsWithIO(ctx.cwd, args, ctx.stdout)
		}, true
	case "repo":
		if len(argv) == 1 {
			return nil, false
		}
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceRepoHandler(ctx.cwd, args, ctx.stdout, ctx.stderr)
		}, true
	case "cleanup":
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceCleanupHandler(args, ctx.stdout, ctx.stderr)
		}, true
	case "review-rules":
		if len(argv) == 1 {
			return nil, false
		}
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceGithubReviewRulesHandler(ctx.cwd, args, ctx.stdout, ctx.stderr)
		}, true
	case "review":
		if len(argv) == 1 {
			return nil, false
		}
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceGithubReviewHandler(ctx.cwd, args, ctx.stdout, ctx.stderr)
		}, true
	case "issue":
		if len(argv) < 2 {
			return nil, false
		}
		switch argv[1] {
		case "implement", "investigate", "sync":
		default:
			return nil, false
		}
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceGithubIssueHandler(ctx.cwd, args, ctx.stdout, ctx.stderr)
		}, true
	case "implement", "sync":
		command := argv[0]
		return func(ctx nanaServiceExecContext, args []string) error {
			return nanaServiceGithubIssueHandler(ctx.cwd, append([]string{command}, args...), ctx.stdout, ctx.stderr)
		}, true
	case "work":
		if len(argv) < 2 {
			return nil, false
		}
		switch argv[1] {
		case "start", "resume", "resolve", "status", "logs", "retrospective", "explain", "verify-refresh", "sync", "lane-exec":
		default:
			return nil, false
		}
		return func(ctx nanaServiceExecContext, args []string) error {
			if len(args) == 0 {
				return errors.New("work service request requires a subcommand")
			}
			switch args[0] {
			case "start":
				return nanaServiceWorkStartHandler(ctx.cwd, args, ctx.stdout, ctx.stderr)
			case "resume":
				return nanaServiceWorkResumeHandler(ctx.cwd, args[1:], ctx.stdout, ctx.stderr)
			case "resolve":
				return nanaServiceWorkResolveHandler(ctx.cwd, args[1:], ctx.stdout, ctx.stderr)
			case "status":
				return nanaServiceWorkStatusHandler(ctx.cwd, args[1:], ctx.stdout)
			case "logs":
				return nanaServiceWorkLogsHandler(ctx.cwd, args[1:], ctx.stdout)
			case "retrospective":
				return nanaServiceWorkRetrospectiveHandler(ctx.cwd, args[1:], ctx.stdout, ctx.stderr)
			case "explain":
				return nanaServiceWorkExplainHandler(args[1:], ctx.stdout)
			case "verify-refresh":
				return nanaServiceWorkVerifyRefreshHandler(ctx.cwd, args[1:], ctx.stdout)
			case "sync":
				return nanaServiceWorkSyncHandler(ctx.cwd, args[1:], ctx.stdout, ctx.stderr)
			case "lane-exec":
				return nanaServiceWorkLaneExecHandler(ctx.cwd, args[1:], ctx.stdout, ctx.stderr)
			default:
				return errors.New("work subcommand is not in the in-process cohort")
			}
		}, true
	default:
		return nil, false
	}
}

func isNanaServiceInternalProcess() bool {
	return strings.TrimSpace(os.Getenv(nanaServiceInternalEnv)) == "1"
}

func nanaServiceUnavailableError() error {
	return errors.New("Nana service is not running for this user. Start it with `nana start`.")
}

func nanaServiceRuntimeDir() string {
	if override := strings.TrimSpace(os.Getenv(nanaServiceRuntimeDirOverrideEnv)); override != "" {
		return override
	}
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "nana")
	}
	return filepath.Join(os.TempDir(), "nana-"+nanaServiceUID())
}

func nanaServiceSocketPath() string {
	return filepath.Join(nanaServiceRuntimeDir(), nanaServiceSocketName)
}

func nanaServiceMetadataPath() string {
	return filepath.Join(nanaServiceRuntimeDir(), nanaServiceMetadataName)
}

func nanaServiceUID() string {
	if current, err := user.Current(); err == nil {
		if trimmed := strings.TrimSpace(current.Uid); trimmed != "" {
			return trimmed
		}
		if trimmed := strings.TrimSpace(current.Username); trimmed != "" {
			return sanitizePathToken(trimmed)
		}
	}
	if value := strings.TrimSpace(os.Getenv("UID")); value != "" {
		return sanitizePathToken(value)
	}
	return "user"
}

func prepareNanaServiceSocketPath(socketPath string, metadataPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return os.Remove(socketPath)
	}
	liveMetadata, live := queryLiveNanaService(socketPath)
	if live {
		_ = writeGithubJSON(metadataPath, liveMetadata)
		return fmt.Errorf("Nana service already active at %s (pid=%d started_at=%s)", socketPath, liveMetadata.ProcessID, liveMetadata.StartedAt)
	}
	if metadata, err := readNanaServiceMetadata(metadataPath); err == nil {
		if metadata.ProcessID > 0 && processAlive(metadata.ProcessID) {
			return fmt.Errorf("Nana service already active at %s (pid=%d started_at=%s)", socketPath, metadata.ProcessID, metadata.StartedAt)
		}
	}
	return os.Remove(socketPath)
}

func readNanaServiceMetadata(path string) (nanaServiceMetadata, error) {
	var metadata nanaServiceMetadata
	err := readGithubJSON(path, &metadata)
	return metadata, err
}

func queryLiveNanaService(socketPath string) (nanaServiceMetadata, bool) {
	conn, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if err != nil {
		return nanaServiceMetadata{}, false
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(nanaServiceRequest{Type: nanaServiceRequestTypeOwnerInfo}); err != nil {
		return nanaServiceMetadata{}, false
	}
	var event nanaServiceEvent
	if err := json.NewDecoder(conn).Decode(&event); err != nil {
		return nanaServiceMetadata{}, false
	}
	if event.Type != nanaServiceEventTypeOwnerInfo || event.Metadata == nil {
		return nanaServiceMetadata{}, false
	}
	return *event.Metadata, true
}
