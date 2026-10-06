// Package commands serves the state-changing command API (§9, job-runner
// spec): POST /api/commands/{name} with a JSON body and a required
// Idempotency-Key header. Each command's response is stored in
// command_requests in the same transaction as its effects, so a repeated key
// replays the original outcome and never repeats an effect.
//
// The dispatcher owns cancel-job; every other command is registered by the
// package that implements it (Handler.Register).
package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/web/apierr"
)

// CancelJob is the name of the command the dispatcher itself serves, the
// {name} segment of POST /api/commands/{name}.
const CancelJob = "cancel-job"

// maxKeyLen bounds the Idempotency-Key header.
const maxKeyLen = 200

// Options configures New.
type Options struct {
	Store *store.Store
	Jobs  *jobs.Runner
	// MaxBodyBytes caps request bodies (server.max_request_bytes). Default 1 MiB.
	MaxBodyBytes int64
	Logger       *slog.Logger
}

// Operation is one decoded command request.
type Operation interface {
	// Canonical is the normalized request body covered by the payload digest:
	// two requests with equal canonical bodies are the same request.
	Canonical() []byte
	// Prepare runs the checks that need no write transaction.
	Prepare(ctx context.Context) error
	// Apply performs the effects and returns the response status and body. ctx
	// is the request context (authenticated session, client address). A
	// *domain.Error answers with its code; any other error is internal.
	Apply(ctx context.Context, tx *jobs.Tx) (status int, body any, err error)
}

// Decoder decodes the JSON body of one command. A *domain.Error answers with
// its code (usually invalid_request) before anything is recorded.
type Decoder func(body []byte) (Operation, error)

// Handler serves POST /api/commands/{name}; the route pattern must bind
// {name}. Unknown command names answer 404 not_found. Authentication and CSRF
// checks are the caller's middleware.
type Handler struct {
	store    *store.Store
	jobs     *jobs.Runner
	maxBody  int64
	log      *slog.Logger
	decoders map[string]Decoder
}

// New returns the command handler with cancel-job registered.
func New(o Options) *Handler {
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 1 << 20
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	h := &Handler{store: o.Store, jobs: o.Jobs, maxBody: o.MaxBodyBytes, log: o.Logger, decoders: map[string]Decoder{}}
	h.Register(CancelJob, decodeCancelJob)
	return h
}

// Register installs the decoder of command name. Register every command
// before the handler serves its first request; registering a name twice
// panics.
func (h *Handler) Register(name string, decode Decoder) {
	if _, dup := h.decoders[name]; dup {
		panic("commands: command " + name + " registered twice")
	}
	h.decoders[name] = decode
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	decode, ok := h.decoders[name]
	if !ok {
		apierr.Write(w, http.StatusNotFound, domain.CodeNotFound, "unknown command")
		return
	}

	key := r.Header.Get("Idempotency-Key")
	if !validKey(key) {
		apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest,
			"the Idempotency-Key header is required (1-200 visible ASCII characters)")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierr.Write(w, http.StatusRequestEntityTooLarge, domain.CodeRequestTooLarge, "request body too large")
			return
		}
		apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "unreadable request body")
		return
	}
	op, err := decode(raw)
	if err != nil {
		h.fail(w, err)
		return
	}
	digest := payloadDigest(name, op.Canonical())
	ctx := r.Context()

	// A known key replays without consulting current state, so the outcome
	// does not depend on what changed since the first request.
	prior, found, err := lookup(ctx, h.store.Reader(), key)
	if err != nil {
		h.fail(w, err)
		return
	}
	if found {
		h.replay(w, prior, digest)
		return
	}
	if err := op.Prepare(ctx); err != nil {
		h.fail(w, err)
		return
	}

	var out stored
	err = h.jobs.Write(ctx, func(tx *jobs.Tx) error {
		var err error
		if out, found, err = lookup(ctx, tx.SQL(), key); err != nil || found {
			return err
		}
		status, body, err := op.Apply(ctx, tx)
		if err != nil {
			return err
		}
		resp, err := json.Marshal(body)
		if err != nil {
			return err
		}
		out = stored{digest: digest, status: status, response: resp}
		_, err = tx.SQL().ExecContext(ctx, `INSERT INTO command_requests
			(idempotency_key, command, payload_digest, status_code, response, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, key, name, digest[:], status, string(resp), clock.Millis(tx.Now()))
		return err
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	h.replay(w, out, digest)
}

// replay writes a stored outcome, or 409 when the key was used for a
// different payload.
func (h *Handler) replay(w http.ResponseWriter, s stored, digest [sha256.Size]byte) {
	if s.digest != digest {
		apierr.Write(w, http.StatusConflict, domain.CodeIdempotencyKeyReused,
			"the Idempotency-Key was already used with a different command or payload")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(s.status)
	_, _ = w.Write(s.response)
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		h.log.Error("commands: internal error", "err", err)
	}
	apierr.FromError(w, err)
}

func validKey(k string) bool {
	if k == "" || len(k) > maxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// payloadDigest is SHA-256 over the command name, a NUL, and the canonical body.
func payloadDigest(name string, canonical []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write(canonical)
	var d [sha256.Size]byte
	h.Sum(d[:0])
	return d
}

type stored struct {
	digest   [sha256.Size]byte
	status   int
	response []byte
}

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func lookup(ctx context.Context, q rowQueryer, key string) (stored, bool, error) {
	var (
		s        stored
		digest   []byte
		response string
	)
	err := q.QueryRowContext(ctx, `SELECT payload_digest, status_code, response
		FROM command_requests WHERE idempotency_key = ?`, key).Scan(&digest, &s.status, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, err
	}
	copy(s.digest[:], digest)
	s.response = []byte(response)
	return s, true, nil
}

// DecodeStrict decodes exactly one JSON object with only known fields into v.
// Any failure is a domain invalid_request error.
func DecodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Errorf(domain.CodeInvalidRequest, "invalid JSON body: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return domain.Errorf(domain.CodeInvalidRequest, "invalid JSON body: trailing data")
	}
	return nil
}

// cancel-job: {"job_id": "<id>"}.

type cancelJobRequest struct {
	JobID string `json:"job_id"`
}

type cancelJobOp struct {
	req cancelJobRequest
	id  domain.JobID
}

func decodeCancelJob(body []byte) (Operation, error) {
	var req cancelJobRequest
	if err := DecodeStrict(body, &req); err != nil {
		return nil, err
	}
	if req.JobID == "" {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "job_id is required")
	}
	return &cancelJobOp{req: req}, nil
}

func (o *cancelJobOp) Canonical() []byte {
	b, err := json.Marshal(o.req)
	if err != nil {
		panic(err) // a struct of one string always marshals
	}
	return b
}

func (o *cancelJobOp) Prepare(context.Context) error {
	id, err := domain.ParseJobID(o.req.JobID)
	o.id = id
	return err
}

func (o *cancelJobOp) Apply(_ context.Context, tx *jobs.Tx) (int, any, error) {
	rec, err := tx.Cancel(o.id)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, rec.Accepted(false), nil
}
