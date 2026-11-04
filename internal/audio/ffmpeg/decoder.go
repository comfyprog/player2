// Package ffmpeg decodes audio files to raw PCM with an FFmpeg subprocess.
//
// Input is restricted to local files: FFmpeg is started with
// "-protocol_whitelist file" and never invoked for remote URLs. The decoder
// writes signed 16-bit little-endian samples to stdout and requires no sound
// device or cgo.
package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"player2/internal/audio"
)

const (
	// defaultSampleRate and defaultChannels are used when a Request leaves
	// them unset or sets them to a non-positive value.
	defaultSampleRate = 48000
	defaultChannels   = 2

	// maxStderrBytes bounds the captured stderr so a chatty or broken decoder
	// cannot grow memory without limit. Only the tail is kept.
	maxStderrBytes = 8 << 10
)

// Decoder decodes local files by running an FFmpeg executable.
type Decoder struct {
	// Bin is the FFmpeg binary to execute. An empty value means "ffmpeg".
	Bin string
}

// New returns a decoder that runs bin. An empty bin means "ffmpeg".
func New(bin string) *Decoder {
	return &Decoder{Bin: bin}
}

// Open starts FFmpeg for req and returns a stream of raw s16le PCM.
//
// Non-positive SampleRate and Channels values default to 48000 and 2.
// Negative Start values are treated as zero. Path is validated locally;
// remote URLs are rejected before any process is started.
func (d *Decoder) Open(ctx context.Context, req audio.Request) (audio.Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	local, err := audio.LocalPath(req.Path)
	if err != nil {
		return nil, err
	}
	req.Path = local
	if req.SampleRate <= 0 {
		req.SampleRate = defaultSampleRate
	}
	if req.Channels <= 0 {
		req.Channels = defaultChannels
	}
	if req.Start < 0 {
		req.Start = 0
	}

	bin := d.binary()
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("ffmpeg: executable %q not found for %q; install FFmpeg or configure the ffmpeg binary: %w", bin, req.Path, err)
	}

	cmd := exec.CommandContext(ctx, bin, buildArgs(req)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	// Kill the entire process group, not just the direct child, when the
	// caller's context is canceled.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: stdout pipe for %q (binary %q): %w", req.Path, bin, err)
	}

	stderr := newBoundedBuffer(maxStderrBytes)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, startError(bin, req.Path, err, stderr.String())
	}

	return &stream{
		stdout: stdout,
		cmd:    cmd,
		stderr: stderr,
		path:   req.Path,
		bin:    bin,
		ctx:    ctx,
	}, nil
}

func (d *Decoder) binary() string {
	if d == nil || d.Bin == "" {
		return "ffmpeg"
	}
	return d.Bin
}

// buildArgs returns the FFmpeg arguments after the binary. The path is one
// argv element, so spaces, quotes, and leading dashes are never
// shell-interpreted.
func buildArgs(req audio.Request) []string {
	rate := req.SampleRate
	if rate <= 0 {
		rate = defaultSampleRate
	}
	channels := req.Channels
	if channels <= 0 {
		channels = defaultChannels
	}

	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-protocol_whitelist", "file",
		"-ss", formatSeconds(req.Start),
		"-i", req.Path,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(channels),
		"pipe:1",
	}
}

// formatSeconds renders a duration for -ss as decimal seconds with at most six
// fractional digits, rounded to the nearest microsecond and with trailing
// zeros trimmed. A zero or negative duration is "0". Emitting -ss even for
// zero keeps the argument list deterministic.
func formatSeconds(d time.Duration) string {
	if d <= 0 {
		return "0"
	}

	micros := int64(d / time.Microsecond)
	if d%time.Microsecond >= 500*time.Nanosecond {
		micros++
	}
	seconds := micros / 1_000_000
	fraction := micros % 1_000_000
	if fraction == 0 {
		return strconv.FormatInt(seconds, 10)
	}
	return strings.TrimRight(fmt.Sprintf("%d.%06d", seconds, fraction), "0")
}

func startError(bin, path string, err error, detail string) error {
	if detail != "" {
		return fmt.Errorf("ffmpeg: start %q for %q: %w: %s", bin, path, err, detail)
	}
	return fmt.Errorf("ffmpeg: start %q for %q: %w", bin, path, err)
}

// stream is an audio.Stream backed by an FFmpeg process and its stdout pipe.
type stream struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
	stderr *boundedBuffer
	ctx    context.Context
	path   string
	bin    string

	waitOnce sync.Once
	waitErr  error
	ctxErr   error

	mu     sync.Mutex
	closed bool
	eof    bool
}

// Read reads PCM from the child. At EOF it reaps the process, so even callers
// that never call Close cannot leave a zombie behind.
func (s *stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.eof {
		s.mu.Unlock()
		return 0, io.EOF
	}
	s.mu.Unlock()

	n, err := s.stdout.Read(p)
	if err != io.EOF {
		return n, err
	}

	s.finalize()

	s.mu.Lock()
	s.eof = true
	s.mu.Unlock()

	if s.waitErr != nil {
		return n, s.decodeError(s.waitErr)
	}
	if s.ctxErr != nil {
		return n, s.decodeError(s.ctxErr)
	}
	return n, io.EOF
}

// Close stops the decoder. It is idempotent: the pipe is closed, the process
// group is killed, and Wait is called exactly once. After Close, ProcessState
// is always non-nil and further reads fail instead of panicking.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	_ = s.stdout.Close()

	if s.cmd.Process != nil && s.cmd.ProcessState == nil {
		if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			// The group may already be gone; fall back to the direct child.
			_ = s.cmd.Process.Kill()
		}
	}

	s.finalize()
	if s.waitErr != nil {
		return s.decodeError(s.waitErr)
	}
	if s.ctxErr != nil {
		return s.decodeError(s.ctxErr)
	}
	return nil
}

// finalize waits for the child exactly once. It records the context error at
// the moment the process ends so cancellation is reported to Read/Close
// instead of masquerading as a clean EOF. The expected SIGKILL produced by
// Close or cancellation itself is ignored; other wait errors are retained.
func (s *stream) finalize() {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		if wasKilled(err) {
			err = nil
		}
		s.waitErr = err
		s.ctxErr = s.ctx.Err()
	})
}

func (s *stream) decodeError(err error) error {
	if detail := s.stderr.String(); detail != "" {
		return fmt.Errorf("ffmpeg: decoding %q with %q: %w: %s", s.path, s.bin, err, detail)
	}
	return fmt.Errorf("ffmpeg: decoding %q with %q: %w", s.path, s.bin, err)
}

// wasKilled reports whether err is the SIGKILL exit status produced when a
// stream is closed or canceled.
func wasKilled(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return status.Signaled() && status.Signal() == syscall.SIGKILL
}

// boundedBuffer keeps at most max bytes of the most recent output. FFmpeg is
// run at -loglevel error, so in normal operation it stays empty.
type boundedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(p)
	if b.max <= 0 {
		return n, nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	if overflow := len(b.buf) + len(p) - b.max; overflow > 0 {
		b.buf = append(b.buf[:0], b.buf[overflow:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
