package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// objectReader streams object contents from one long-lived `git cat-file
// --batch` process.
//
// Reading each blob with its own `git cat-file blob` invocation costs one
// process per tracked file, and that fork cost dominates snapshot time: on a
// 311-file repository it is seconds, against tens of milliseconds for a single
// batched process. Requests and responses are interleaved one object at a time
// so neither pipe can fill while both sides wait on the other.
type objectReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
	ctx    context.Context
	failed bool
}

// newObjectReader starts the batch process for one repository root.
func (c *Client) newObjectReader(ctx context.Context, root string) (*objectReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.executable, "cat-file", "--batch")
	cmd.Dir = root
	cmd.Env = commandEnvironment()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open git cat-file input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open git cat-file output: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, &CommandError{Args: []string{"cat-file", "--batch"}, Stderr: stderr.String(), Err: err}
	}
	return &objectReader{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr, ctx: ctx}, nil
}

// object returns the full contents of one object. The batch protocol answers
// each request with a `<oid> <type> <size>` header, exactly size bytes, and a
// trailing newline.
func (r *objectReader) object(name string) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if r.failed {
		return nil, errors.New("git cat-file batch is no longer usable")
	}
	if strings.ContainsAny(name, "\n\r") {
		return nil, fmt.Errorf("invalid git object name %q", name)
	}
	if _, err := io.WriteString(r.stdin, name+"\n"); err != nil {
		return nil, r.fail(fmt.Errorf("request git object %q: %w", name, err))
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return nil, r.fail(fmt.Errorf("read git object header for %q: %w", name, err))
	}
	header = strings.TrimSuffix(header, "\n")
	fields := strings.Fields(header)
	// A missing or ambiguous object answers with a two-field status line
	// instead of a header, and no payload follows it.
	if len(fields) == 2 {
		return nil, r.fail(fmt.Errorf("git object %q is %s", name, fields[1]))
	}
	if len(fields) != 3 {
		return nil, r.fail(fmt.Errorf("malformed git cat-file header %q", header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return nil, r.fail(fmt.Errorf("malformed git object size in %q", header))
	}
	contents := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, contents); err != nil {
		return nil, r.fail(fmt.Errorf("read git object %q: %w", name, err))
	}
	// The trailing newline separates payloads; without consuming it the next
	// header read would start one byte late.
	if _, err := r.stdout.ReadByte(); err != nil {
		return nil, r.fail(fmt.Errorf("read git object terminator for %q: %w", name, err))
	}
	return contents, nil
}

// fail marks the reader unusable, because a protocol error leaves the stream
// at an unknown offset and every later response would be misread.
func (r *objectReader) fail(cause error) error {
	r.failed = true
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return cause
}

// Close ends the batch process and reports a non-zero exit.
func (r *objectReader) Close() error {
	if r == nil || r.cmd == nil {
		return nil
	}
	closeErr := r.stdin.Close()
	waitErr := r.cmd.Wait()
	r.cmd = nil
	if waitErr != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// A reader abandoned after a protocol error is expected to end with a
		// broken pipe, so only a clean run reports the exit status.
		if !r.failed {
			return &CommandError{Args: []string{"cat-file", "--batch"}, Stderr: r.stderr.String(), Err: waitErr}
		}
	}
	if closeErr != nil && !r.failed {
		return fmt.Errorf("close git cat-file input: %w", closeErr)
	}
	return nil
}
