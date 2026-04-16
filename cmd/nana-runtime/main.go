package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const runtimeSchemaVersion = 1

var runtimeCommandNames = []string{
	"acquire-authority",
	"renew-authority",
	"queue-dispatch",
	"mark-notified",
	"mark-delivered",
	"mark-failed",
	"request-replay",
	"capture-snapshot",
	"create-mailbox-message",
	"mark-mailbox-notified",
	"mark-mailbox-delivered",
}

var runtimeEventNames = []string{
	"authority-acquired",
	"authority-renewed",
	"dispatch-queued",
	"dispatch-notified",
	"dispatch-delivered",
	"dispatch-failed",
	"replay-requested",
	"snapshot-captured",
	"mailbox-message-created",
	"mailbox-notified",
	"mailbox-delivered",
}

type authoritySnapshot struct {
	Owner       *string `json:"owner"`
	LeaseID     *string `json:"lease_id"`
	LeasedUntil *string `json:"leased_until"`
	Stale       bool    `json:"stale"`
	StaleReason *string `json:"stale_reason"`
}

type backlogSnapshot struct {
	Pending   uint64 `json:"pending"`
	Notified  uint64 `json:"notified"`
	Delivered uint64 `json:"delivered"`
	Failed    uint64 `json:"failed"`
}

type replaySnapshot struct {
	Cursor                     *string `json:"cursor"`
	PendingEvents              uint64  `json:"pending_events"`
	LastReplayedEventID        *string `json:"last_replayed_event_id"`
	DeferredLeaderNotification bool    `json:"deferred_leader_notification"`
}

type readinessSnapshot struct {
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons"`
}

type runtimeSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	Authority     authoritySnapshot `json:"authority"`
	Backlog       backlogSnapshot   `json:"backlog"`
	Replay        replaySnapshot    `json:"replay"`
	Readiness     readinessSnapshot `json:"readiness"`
}

type dispatchStatus string

const (
	dispatchPending   dispatchStatus = "pending"
	dispatchNotified  dispatchStatus = "notified"
	dispatchDelivered dispatchStatus = "delivered"
	dispatchFailed    dispatchStatus = "failed"
)

type dispatchRecord struct {
	RequestID   string          `json:"request_id"`
	Target      string          `json:"target"`
	Status      dispatchStatus  `json:"status"`
	CreatedAt   string          `json:"created_at"`
	NotifiedAt  *string         `json:"notified_at"`
	DeliveredAt *string         `json:"delivered_at"`
	FailedAt    *string         `json:"failed_at"`
	Reason      *string         `json:"reason"`
	Metadata    json.RawMessage `json:"metadata"`
}

type dispatchLog struct {
	Records []dispatchRecord `json:"records"`
}

type mailboxRecord struct {
	MessageID   string  `json:"message_id"`
	FromWorker  string  `json:"from_worker"`
	ToWorker    string  `json:"to_worker"`
	Body        string  `json:"body"`
	CreatedAt   string  `json:"created_at"`
	NotifiedAt  *string `json:"notified_at"`
	DeliveredAt *string `json:"delivered_at"`
}

type mailboxLog struct {
	Records []mailboxRecord `json:"records"`
}

type authorityLease struct {
	Owner       *string
	LeaseID     *string
	LeasedUntil *string
	Stale       bool
	StaleReason *string
}

type replayState struct {
	Cursor                     *string
	SeenEventIDs               map[string]struct{}
	DeferredLeaderNotification bool
}

type runtimeDirtyState struct {
	Snapshot  bool
	Events    bool
	Authority bool
	Backlog   bool
	Replay    bool
	Readiness bool
	Dispatch  bool
	Mailbox   bool
}

type runtimeLoadOptions struct {
	LoadEventLog bool
}

type runtimeEngine struct {
	Authority      authorityLease
	Dispatch       dispatchLog
	Mailbox        mailboxLog
	Replay         replayState
	EventLog       []map[string]any
	PendingEvents  []map[string]any
	EventLogLoaded bool
	StateDir       string
	Dirty          runtimeDirtyState
}

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "nana-runtime: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	switch first {
	case "", "--help", "-h":
		printUsage()
		return nil
	case "schema":
		if len(args) > 1 && args[1] == "--json" {
			payload := map[string]any{
				"schema_version": runtimeSchemaVersion,
				"commands":       runtimeCommandNames,
				"events":         runtimeEventNames,
				"transport":      "tmux",
			}
			return printJSON(payload)
		}
		fmt.Fprintln(os.Stdout, runtimeContractSummary())
		return nil
	case "snapshot":
		stateDir := parseStateDir(args[1:])
		engine := newRuntimeEngine()
		var err error
		if stateDir != "" {
			engine, err = loadRuntimeEngine(stateDir)
			if err != nil {
				return err
			}
		}
		snapshot := engine.snapshot()
		if slices.Contains(args[1:], "--json") {
			return printJSON(snapshot)
		}
		fmt.Fprintln(os.Stdout, formatSnapshot(snapshot))
		return nil
	case "mux-contract":
		fmt.Fprintln(os.Stdout, "adapter-status=tmux adapter ready")
		fmt.Fprintln(os.Stdout, canonicalMuxContractSummary())
		fmt.Fprintln(os.Stdout, "sample-operation=cannot operate on a detached target")
		return nil
	case "exec":
		if len(args) < 2 {
			return errors.New("exec requires a JSON command argument")
		}
		stateDir := parseStateDir(args[2:])
		compact := slices.Contains(args[2:], "--compact")
		engine := newRuntimeEngine()
		var err error
		if stateDir != "" {
			engine, err = loadRuntimeEngineWithOptions(stateDir, runtimeLoadOptions{LoadEventLog: compact})
			if err != nil {
				engine = newRuntimeEngine()
			}
			engine.StateDir = stateDir
		}
		event, err := engine.processRawCommand(args[1])
		if err != nil {
			return err
		}
		if compact {
			engine.compact()
		}
		if stateDir != "" {
			if err := engine.persist(); err != nil {
				return err
			}
			if err := engine.writeCompatibilityView(); err != nil {
				return err
			}
		}
		return printJSON(event)
	case "init":
		if len(args) < 2 {
			return errors.New("init requires a state directory path")
		}
		engine := newRuntimeEngine()
		engine.StateDir = args[1]
		if err := engine.persist(); err != nil {
			return err
		}
		if err := engine.writeCompatibilityView(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "initialized state directory: %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown subcommand `%s`", first)
	}
}

func printUsage() {
	fmt.Fprint(os.Stdout, ""+
		"usage: nana-runtime <command> [options]\n\n"+
		"commands:\n"+
		"  schema [--json]                     print the runtime contract summary\n"+
		"  snapshot [--json] [--state-dir=DIR]  print a runtime snapshot\n"+
		"  mux-contract                        print the mux boundary summary\n"+
		"  exec <json> [--state-dir=DIR]       process a runtime command from JSON\n"+
		"  init <state-dir>                    initialize a fresh state directory\n")
}

func parseStateDir(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--state-dir=") {
			return strings.TrimPrefix(arg, "--state-dir=")
		}
	}
	return ""
}

func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s\n", encoded)
	return nil
}

func runtimeContractSummary() string {
	return fmt.Sprintf(
		"runtime-schema=%d\ncommands=%s\nevents=%s\ntransport=tmux\nqueue-transition=notified\nsnapshot=authority, backlog, replay, readiness",
		runtimeSchemaVersion,
		strings.Join(runtimeCommandNames, ", "),
		strings.Join(runtimeEventNames, ", "),
	)
}

func canonicalMuxContractSummary() string {
	return "mux-operations=resolve-target, send-input, capture-tail, inspect-liveness, attach, detach\nmux-target-kinds=delivery-handle, detached\nsubmit-policy=enter(presses=2, delay_ms=100)\nreadiness=Ok\nconfirmation=Confirmed\nadapter=tmux"
}

func newRuntimeEngine() *runtimeEngine {
	return &runtimeEngine{
		Dispatch: dispatchLog{Records: []dispatchRecord{}},
		Mailbox:  mailboxLog{Records: []mailboxRecord{}},
		Replay: replayState{
			SeenEventIDs: map[string]struct{}{},
		},
		EventLog:      []map[string]any{},
		PendingEvents: []map[string]any{},
	}
}

func (e *runtimeEngine) processRawCommand(raw string) (map[string]any, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var command string
	if err := json.Unmarshal(payload["command"], &command); err != nil {
		return nil, fmt.Errorf("invalid JSON: missing command")
	}
	switch command {
	case "AcquireAuthority":
		var owner, leaseID, leasedUntil string
		if err := decodeFields(payload, map[string]*string{
			"owner":        &owner,
			"lease_id":     &leaseID,
			"leased_until": &leasedUntil,
		}); err != nil {
			return nil, err
		}
		if err := e.acquireAuthority(owner, leaseID, leasedUntil); err != nil {
			return nil, err
		}
		e.markAuthorityStateChanged()
		return e.appendEvent(map[string]any{"event": "AuthorityAcquired", "owner": owner, "lease_id": leaseID, "leased_until": leasedUntil}), nil
	case "RenewAuthority":
		var owner, leaseID, leasedUntil string
		if err := decodeFields(payload, map[string]*string{
			"owner":        &owner,
			"lease_id":     &leaseID,
			"leased_until": &leasedUntil,
		}); err != nil {
			return nil, err
		}
		if err := e.renewAuthority(owner, leaseID, leasedUntil); err != nil {
			return nil, err
		}
		e.markAuthorityStateChanged()
		return e.appendEvent(map[string]any{"event": "AuthorityRenewed", "owner": owner, "lease_id": leaseID, "leased_until": leasedUntil}), nil
	case "QueueDispatch":
		var requestID, target string
		if err := decodeFields(payload, map[string]*string{
			"request_id": &requestID,
			"target":     &target,
		}); err != nil {
			return nil, err
		}
		metadata := payload["metadata"]
		e.queueDispatch(requestID, target, metadata)
		e.markDispatchStateChanged()
		event := map[string]any{"event": "DispatchQueued", "request_id": requestID, "target": target}
		if len(metadata) > 0 && string(metadata) != "null" {
			var value any
			_ = json.Unmarshal(metadata, &value)
			event["metadata"] = value
		}
		return e.appendEvent(event), nil
	case "MarkNotified":
		var requestID, channel string
		if err := decodeFields(payload, map[string]*string{
			"request_id": &requestID,
			"channel":    &channel,
		}); err != nil {
			return nil, err
		}
		if err := e.markNotified(requestID, channel); err != nil {
			return nil, err
		}
		e.markDispatchStateChanged()
		return e.appendEvent(map[string]any{"event": "DispatchNotified", "request_id": requestID, "channel": channel}), nil
	case "MarkDelivered":
		var requestID string
		if err := decodeFields(payload, map[string]*string{"request_id": &requestID}); err != nil {
			return nil, err
		}
		if err := e.markDelivered(requestID); err != nil {
			return nil, err
		}
		e.markDispatchStateChanged()
		return e.appendEvent(map[string]any{"event": "DispatchDelivered", "request_id": requestID}), nil
	case "MarkFailed":
		var requestID, reason string
		if err := decodeFields(payload, map[string]*string{
			"request_id": &requestID,
			"reason":     &reason,
		}); err != nil {
			return nil, err
		}
		if err := e.markFailed(requestID, reason); err != nil {
			return nil, err
		}
		e.markDispatchStateChanged()
		return e.appendEvent(map[string]any{"event": "DispatchFailed", "request_id": requestID, "reason": reason}), nil
	case "RequestReplay":
		var cursor *string
		if rawCursor, ok := payload["cursor"]; ok && len(rawCursor) > 0 && string(rawCursor) != "null" {
			var value string
			if err := json.Unmarshal(rawCursor, &value); err != nil {
				return nil, err
			}
			cursor = &value
		}
		e.Replay.Cursor = cursor
		e.markReplayStateChanged()
		event := map[string]any{"event": "ReplayRequested", "cursor": nil}
		if cursor != nil {
			event["cursor"] = *cursor
		}
		return e.appendEvent(event), nil
	case "CaptureSnapshot":
		return e.appendEvent(map[string]any{"event": "SnapshotCaptured"}), nil
	case "CreateMailboxMessage":
		var messageID, fromWorker, toWorker, body string
		if err := decodeFields(payload, map[string]*string{
			"message_id":  &messageID,
			"from_worker": &fromWorker,
			"to_worker":   &toWorker,
			"body":        &body,
		}); err != nil {
			return nil, err
		}
		e.createMailboxMessage(messageID, fromWorker, toWorker, body)
		e.markMailboxStateChanged()
		return e.appendEvent(map[string]any{"event": "MailboxMessageCreated", "message_id": messageID, "from_worker": fromWorker, "to_worker": toWorker, "body": body}), nil
	case "MarkMailboxNotified":
		var messageID string
		if err := decodeFields(payload, map[string]*string{"message_id": &messageID}); err != nil {
			return nil, err
		}
		if err := e.markMailboxNotified(messageID); err != nil {
			return nil, err
		}
		e.markMailboxStateChanged()
		return e.appendEvent(map[string]any{"event": "MailboxNotified", "message_id": messageID}), nil
	case "MarkMailboxDelivered":
		var messageID string
		if err := decodeFields(payload, map[string]*string{"message_id": &messageID}); err != nil {
			return nil, err
		}
		if err := e.markMailboxDelivered(messageID); err != nil {
			return nil, err
		}
		e.markMailboxStateChanged()
		return e.appendEvent(map[string]any{"event": "MailboxDelivered", "message_id": messageID}), nil
	default:
		return nil, fmt.Errorf("unknown command: %s", command)
	}
}

func decodeFields(payload map[string]json.RawMessage, fields map[string]*string) error {
	for key, target := range fields {
		raw, ok := payload[key]
		if !ok {
			return fmt.Errorf("invalid JSON: missing %s", key)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	}
	return nil
}

func (e *runtimeEngine) appendEvent(event map[string]any) map[string]any {
	if e.EventLogLoaded {
		e.EventLog = append(e.EventLog, event)
	}
	e.PendingEvents = append(e.PendingEvents, event)
	e.Dirty.Events = true
	return event
}

func (e *runtimeEngine) markAuthorityStateChanged() {
	e.Dirty.Snapshot = true
	e.Dirty.Authority = true
	e.Dirty.Readiness = true
}

func (e *runtimeEngine) markDispatchStateChanged() {
	e.Dirty.Snapshot = true
	e.Dirty.Backlog = true
	e.Dirty.Dispatch = true
}

func (e *runtimeEngine) markReplayStateChanged() {
	e.Dirty.Snapshot = true
	e.Dirty.Replay = true
}

func (e *runtimeEngine) markMailboxStateChanged() {
	e.Dirty.Mailbox = true
}

func (e *runtimeEngine) acquireAuthority(owner string, leaseID string, leasedUntil string) error {
	if e.Authority.Owner != nil && *e.Authority.Owner != owner {
		return fmt.Errorf("authority error: lease already held by %s", *e.Authority.Owner)
	}
	e.Authority.Owner = &owner
	e.Authority.LeaseID = &leaseID
	e.Authority.LeasedUntil = &leasedUntil
	e.Authority.Stale = false
	e.Authority.StaleReason = nil
	return nil
}

func (e *runtimeEngine) renewAuthority(owner string, leaseID string, leasedUntil string) error {
	if e.Authority.Owner == nil {
		return errors.New("authority error: no lease currently held")
	}
	if *e.Authority.Owner != owner {
		return fmt.Errorf("authority error: owner mismatch: lease held by %s", *e.Authority.Owner)
	}
	e.Authority.LeaseID = &leaseID
	e.Authority.LeasedUntil = &leasedUntil
	e.Authority.Stale = false
	e.Authority.StaleReason = nil
	return nil
}

func (e *runtimeEngine) queueDispatch(requestID string, target string, metadata json.RawMessage) {
	record := dispatchRecord{
		RequestID: requestID,
		Target:    target,
		Status:    dispatchPending,
		CreatedAt: nowISO(),
	}
	if len(metadata) > 0 && string(metadata) != "null" {
		record.Metadata = append(json.RawMessage{}, metadata...)
	}
	e.Dispatch.Records = append(e.Dispatch.Records, record)
}

func (e *runtimeEngine) markNotified(requestID string, channel string) error {
	record := e.findDispatch(requestID)
	if record == nil {
		return fmt.Errorf("dispatch error: dispatch record not found: %s", requestID)
	}
	if record.Status != dispatchPending {
		return fmt.Errorf("dispatch error: invalid transition for %s: %s -> %s", requestID, record.Status, dispatchNotified)
	}
	record.Status = dispatchNotified
	value := nowISO()
	record.NotifiedAt = &value
	record.Reason = &channel
	return nil
}

func (e *runtimeEngine) markDelivered(requestID string) error {
	record := e.findDispatch(requestID)
	if record == nil {
		return fmt.Errorf("dispatch error: dispatch record not found: %s", requestID)
	}
	if record.Status != dispatchNotified {
		return fmt.Errorf("dispatch error: invalid transition for %s: %s -> %s", requestID, record.Status, dispatchDelivered)
	}
	record.Status = dispatchDelivered
	value := nowISO()
	record.DeliveredAt = &value
	return nil
}

func (e *runtimeEngine) markFailed(requestID string, reason string) error {
	record := e.findDispatch(requestID)
	if record == nil {
		return fmt.Errorf("dispatch error: dispatch record not found: %s", requestID)
	}
	if record.Status != dispatchPending && record.Status != dispatchNotified {
		return fmt.Errorf("dispatch error: invalid transition for %s: %s -> %s", requestID, record.Status, dispatchFailed)
	}
	record.Status = dispatchFailed
	value := nowISO()
	record.FailedAt = &value
	record.Reason = &reason
	return nil
}

func (e *runtimeEngine) findDispatch(requestID string) *dispatchRecord {
	for index := range e.Dispatch.Records {
		if e.Dispatch.Records[index].RequestID == requestID {
			return &e.Dispatch.Records[index]
		}
	}
	return nil
}

func (e *runtimeEngine) createMailboxMessage(messageID string, fromWorker string, toWorker string, body string) {
	e.Mailbox.Records = append(e.Mailbox.Records, mailboxRecord{
		MessageID:  messageID,
		FromWorker: fromWorker,
		ToWorker:   toWorker,
		Body:       body,
		CreatedAt:  nowISO(),
	})
}

func (e *runtimeEngine) markMailboxNotified(messageID string) error {
	record := e.findMailbox(messageID)
	if record == nil {
		return fmt.Errorf("mailbox error: mailbox record not found: %s", messageID)
	}
	if record.DeliveredAt != nil {
		return fmt.Errorf("mailbox error: mailbox message already delivered: %s", messageID)
	}
	value := nowISO()
	record.NotifiedAt = &value
	return nil
}

func (e *runtimeEngine) markMailboxDelivered(messageID string) error {
	record := e.findMailbox(messageID)
	if record == nil {
		return fmt.Errorf("mailbox error: mailbox record not found: %s", messageID)
	}
	if record.DeliveredAt != nil {
		return fmt.Errorf("mailbox error: mailbox message already delivered: %s", messageID)
	}
	value := nowISO()
	record.DeliveredAt = &value
	return nil
}

func (e *runtimeEngine) findMailbox(messageID string) *mailboxRecord {
	for index := range e.Mailbox.Records {
		if e.Mailbox.Records[index].MessageID == messageID {
			return &e.Mailbox.Records[index]
		}
	}
	return nil
}

func (e *runtimeEngine) snapshot() runtimeSnapshot {
	return runtimeSnapshot{
		SchemaVersion: runtimeSchemaVersion,
		Authority: authoritySnapshot{
			Owner:       e.Authority.Owner,
			LeaseID:     e.Authority.LeaseID,
			LeasedUntil: e.Authority.LeasedUntil,
			Stale:       e.Authority.Stale,
			StaleReason: e.Authority.StaleReason,
		},
		Backlog: e.dispatchBacklog(),
		Replay: replaySnapshot{
			Cursor:                     e.Replay.Cursor,
			PendingEvents:              0,
			LastReplayedEventID:        nil,
			DeferredLeaderNotification: e.Replay.DeferredLeaderNotification,
		},
		Readiness: e.deriveReadiness(),
	}
}

func (e *runtimeEngine) dispatchBacklog() backlogSnapshot {
	var snapshot backlogSnapshot
	for _, record := range e.Dispatch.Records {
		switch record.Status {
		case dispatchPending:
			snapshot.Pending++
		case dispatchNotified:
			snapshot.Notified++
		case dispatchDelivered:
			snapshot.Delivered++
		case dispatchFailed:
			snapshot.Failed++
		}
	}
	return snapshot
}

func (e *runtimeEngine) deriveReadiness() readinessSnapshot {
	reasons := []string{}
	if e.Authority.Owner == nil {
		reasons = append(reasons, "authority lease not acquired")
	} else if e.Authority.Stale {
		detail := ""
		if e.Authority.StaleReason != nil {
			detail = *e.Authority.StaleReason
		}
		reasons = append(reasons, fmt.Sprintf("authority lease is stale: %s", detail))
	}
	if len(reasons) == 0 {
		return readinessSnapshot{Ready: true, Reasons: []string{}}
	}
	return readinessSnapshot{Ready: false, Reasons: reasons}
}

func formatSnapshot(snapshot runtimeSnapshot) string {
	owner := "none"
	if snapshot.Authority.Owner != nil {
		owner = *snapshot.Authority.Owner
	}
	leaseID := "none"
	if snapshot.Authority.LeaseID != nil {
		leaseID = *snapshot.Authority.LeaseID
	}
	leasedUntil := "none"
	if snapshot.Authority.LeasedUntil != nil {
		leasedUntil = *snapshot.Authority.LeasedUntil
	}
	staleReason := "none"
	if snapshot.Authority.StaleReason != nil {
		staleReason = *snapshot.Authority.StaleReason
	}
	readiness := "ready"
	if !snapshot.Readiness.Ready {
		readiness = fmt.Sprintf("blocked(%s)", strings.Join(snapshot.Readiness.Reasons, "; "))
	}
	cursor := "none"
	if snapshot.Replay.Cursor != nil {
		cursor = *snapshot.Replay.Cursor
	}
	lastReplayed := "none"
	if snapshot.Replay.LastReplayedEventID != nil {
		lastReplayed = *snapshot.Replay.LastReplayedEventID
	}
	return fmt.Sprintf(
		"schema=%d authority=owner=%s lease_id=%s leased_until=%s stale=%t stale_reason=%s backlog=pending=%d notified=%d delivered=%d failed=%d replay=cursor=%s pending_events=%d last_replayed_event_id=%s deferred_leader_notification=%t readiness=%s",
		snapshot.SchemaVersion,
		owner,
		leaseID,
		leasedUntil,
		snapshot.Authority.Stale,
		staleReason,
		snapshot.Backlog.Pending,
		snapshot.Backlog.Notified,
		snapshot.Backlog.Delivered,
		snapshot.Backlog.Failed,
		cursor,
		snapshot.Replay.PendingEvents,
		lastReplayed,
		snapshot.Replay.DeferredLeaderNotification,
		readiness,
	)
}

func (e *runtimeEngine) compact() {
	if !e.EventLogLoaded {
		return
	}
	terminal := map[string]struct{}{}
	for _, record := range e.Dispatch.Records {
		if record.Status == dispatchDelivered || record.Status == dispatchFailed {
			terminal[record.RequestID] = struct{}{}
		}
	}
	filtered := make([]map[string]any, 0, len(e.EventLog))
	for _, event := range e.EventLog {
		requestID, _ := event["request_id"].(string)
		if requestID != "" {
			if _, ok := terminal[requestID]; ok {
				continue
			}
		}
		filtered = append(filtered, event)
	}
	e.EventLog = filtered
	e.Dirty.Events = true
}

func (e *runtimeEngine) persist() error {
	if strings.TrimSpace(e.StateDir) == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		return err
	}
	snapshotPath := filepath.Join(e.StateDir, "snapshot.json")
	if e.Dirty.Snapshot || !runtimeFileExists(snapshotPath) {
		if err := writePrettyJSON(snapshotPath, e.snapshot()); err != nil {
			return err
		}
		e.Dirty.Snapshot = false
	}
	eventsPath := filepath.Join(e.StateDir, "events.json")
	if e.Dirty.Events || !runtimeFileExists(eventsPath) {
		if err := e.persistEventLog(eventsPath); err != nil {
			return err
		}
		e.Dirty.Events = false
		e.PendingEvents = nil
	}
	return nil
}

func (e *runtimeEngine) writeCompatibilityView() error {
	if strings.TrimSpace(e.StateDir) == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		return err
	}
	snapshot := e.snapshot()
	writeIfDirty := func(path string, dirty *bool, value any) error {
		if !*dirty && runtimeFileExists(path) {
			return nil
		}
		if err := writePrettyJSON(path, value); err != nil {
			return err
		}
		*dirty = false
		return nil
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "authority.json"), &e.Dirty.Authority, snapshot.Authority); err != nil {
		return err
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "backlog.json"), &e.Dirty.Backlog, snapshot.Backlog); err != nil {
		return err
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "readiness.json"), &e.Dirty.Readiness, snapshot.Readiness); err != nil {
		return err
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "replay.json"), &e.Dirty.Replay, snapshot.Replay); err != nil {
		return err
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "dispatch.json"), &e.Dirty.Dispatch, e.Dispatch); err != nil {
		return err
	}
	if err := writeIfDirty(filepath.Join(e.StateDir, "mailbox.json"), &e.Dirty.Mailbox, e.Mailbox); err != nil {
		return err
	}
	return nil
}

func (e *runtimeEngine) persistEventLog(path string) error {
	if e.EventLogLoaded {
		return writePrettyJSON(path, e.EventLog)
	}
	if len(e.PendingEvents) == 0 {
		return writePrettyJSON(path, []map[string]any{})
	}
	for _, event := range e.PendingEvents {
		if err := appendPrettyJSONArrayValue(path, event); err != nil {
			return err
		}
	}
	return nil
}

func loadRuntimeEngine(stateDir string) (*runtimeEngine, error) {
	return loadRuntimeEngineWithOptions(stateDir, runtimeLoadOptions{})
}

func loadRuntimeEngineWithOptions(stateDir string, options runtimeLoadOptions) (*runtimeEngine, error) {
	engine, err := loadRuntimeEngineFromSnapshot(stateDir)
	if err == nil {
		if options.LoadEventLog {
			events, eventsErr := readRuntimeEventLog(filepath.Join(stateDir, "events.json"))
			if eventsErr != nil {
				return nil, eventsErr
			}
			engine.EventLog = events
			engine.EventLogLoaded = true
		}
		return engine, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return loadRuntimeEngineFromEvents(stateDir, options)
}

func loadRuntimeEngineFromSnapshot(stateDir string) (*runtimeEngine, error) {
	var snapshot runtimeSnapshot
	if err := readRuntimeJSON(filepath.Join(stateDir, "snapshot.json"), &snapshot); err != nil {
		return nil, err
	}
	var dispatch dispatchLog
	if err := readRuntimeJSON(filepath.Join(stateDir, "dispatch.json"), &dispatch); err != nil {
		return nil, err
	}
	var mailbox mailboxLog
	if err := readRuntimeJSON(filepath.Join(stateDir, "mailbox.json"), &mailbox); err != nil {
		return nil, err
	}
	engine := newRuntimeEngine()
	engine.StateDir = stateDir
	engine.Authority = authorityLease{
		Owner:       snapshot.Authority.Owner,
		LeaseID:     snapshot.Authority.LeaseID,
		LeasedUntil: snapshot.Authority.LeasedUntil,
		Stale:       snapshot.Authority.Stale,
		StaleReason: snapshot.Authority.StaleReason,
	}
	engine.Dispatch = dispatch
	engine.Mailbox = mailbox
	engine.Replay.Cursor = snapshot.Replay.Cursor
	engine.Replay.DeferredLeaderNotification = snapshot.Replay.DeferredLeaderNotification
	return engine, nil
}

func loadRuntimeEngineFromEvents(stateDir string, options runtimeLoadOptions) (*runtimeEngine, error) {
	events, err := readRuntimeEventLog(filepath.Join(stateDir, "events.json"))
	if err != nil {
		return nil, err
	}
	engine := newRuntimeEngine()
	engine.StateDir = stateDir
	if options.LoadEventLog {
		engine.EventLog = make([]map[string]any, 0, len(events))
		engine.EventLogLoaded = true
	}
	for _, event := range events {
		replayEvent(engine, event)
		if options.LoadEventLog {
			engine.EventLog = append(engine.EventLog, event)
		}
	}
	return engine, nil
}

func readRuntimeEventLog(path string) ([]map[string]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var events []map[string]any
	if err := json.Unmarshal(content, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func readRuntimeJSON(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func replayEvent(engine *runtimeEngine, event map[string]any) {
	eventType, _ := event["event"].(string)
	switch eventType {
	case "AuthorityAcquired", "AuthorityRenewed":
		owner, _ := event["owner"].(string)
		leaseID, _ := event["lease_id"].(string)
		leasedUntil, _ := event["leased_until"].(string)
		_ = engine.acquireAuthority(owner, leaseID, leasedUntil)
	case "DispatchQueued":
		requestID, _ := event["request_id"].(string)
		target, _ := event["target"].(string)
		var metadata json.RawMessage
		if raw, ok := event["metadata"]; ok {
			encoded, _ := json.Marshal(raw)
			metadata = encoded
		}
		engine.queueDispatch(requestID, target, metadata)
	case "DispatchNotified":
		requestID, _ := event["request_id"].(string)
		channel, _ := event["channel"].(string)
		_ = engine.markNotified(requestID, channel)
	case "DispatchDelivered":
		requestID, _ := event["request_id"].(string)
		_ = engine.markDelivered(requestID)
	case "DispatchFailed":
		requestID, _ := event["request_id"].(string)
		reason, _ := event["reason"].(string)
		_ = engine.markFailed(requestID, reason)
	case "ReplayRequested":
		if value, ok := event["cursor"].(string); ok {
			engine.Replay.Cursor = &value
		} else {
			engine.Replay.Cursor = nil
		}
	case "MailboxMessageCreated":
		messageID, _ := event["message_id"].(string)
		fromWorker, _ := event["from_worker"].(string)
		toWorker, _ := event["to_worker"].(string)
		body, _ := event["body"].(string)
		engine.createMailboxMessage(messageID, fromWorker, toWorker, body)
	case "MailboxNotified":
		messageID, _ := event["message_id"].(string)
		_ = engine.markMailboxNotified(messageID)
	case "MailboxDelivered":
		messageID, _ := event["message_id"].(string)
		_ = engine.markMailboxDelivered(messageID)
	}
}

func writePrettyJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(path, append(encoded, '\n'))
}

func appendPrettyJSONArrayValue(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "  ", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		_, err = file.Write(append(append([]byte("[\n"), encoded...), []byte("\n]\n")...))
		return err
	}
	appendPos, empty, err := findJSONArrayAppendPosition(file, info.Size())
	if err != nil {
		return err
	}
	if _, err := file.Seek(appendPos, 0); err != nil {
		return err
	}
	prefix := []byte(",\n")
	if empty {
		prefix = []byte("\n")
	}
	_, err = file.Write(append(append(prefix, encoded...), []byte("\n]\n")...))
	return err
}

func findJSONArrayAppendPosition(file *os.File, size int64) (int64, bool, error) {
	const chunkSize int64 = 4096
	var significant []byte
	var positions []int64
	for offset := size; offset > 0 && len(significant) < 2; {
		start := offset - chunkSize
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, offset-start)
		if _, err := file.ReadAt(chunk, start); err != nil {
			return 0, false, err
		}
		for index := len(chunk) - 1; index >= 0 && len(significant) < 2; index-- {
			if runtimeJSONWhitespace(chunk[index]) {
				continue
			}
			significant = append(significant, chunk[index])
			positions = append(positions, start+int64(index))
		}
		offset = start
	}
	if len(significant) == 0 || significant[0] != ']' {
		return 0, false, errors.New("invalid events.json: expected JSON array")
	}
	empty := len(significant) > 1 && significant[1] == '['
	return positions[0], empty, nil
}

func runtimeJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func writeFileAtomically(path string, content []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func runtimeFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
