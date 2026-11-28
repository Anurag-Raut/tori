package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const acpAuthPrefix = "Open the following link to authenticate the ACP server: "

var (
	errACPCancelled = errors.New("cancelled")
	errACPTimeout   = errors.New("timed out")
	errACPExited    = errors.New("the runtime exited")
)

// acpError is a JSON-RPC error answer from the runtime.
type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *acpError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type acpReply struct {
	result json.RawMessage
	err    *acpError
}

// acpClient speaks ACP (JSON-RPC 2.0, one JSON object per line) to a runtime.
// Everything the runtime prints on stderr is dropped, except the sign-in link.
type acpClient struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu      sync.Mutex
	nextID  int
	pending map[int]chan acpReply
	closed  bool

	exited chan struct{}
}

// validAuthURL accepts only Google's OAuth consent page.
func validAuthURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" || u.User != nil {
		return "", false
	}
	return u.String(), true
}

// authURLFromLine extracts the sign-in link from a runtime line.
func authURLFromLine(line string) (string, bool) {
	i := strings.Index(line, acpAuthPrefix)
	if i < 0 {
		return "", false
	}
	return validAuthURL(line[i+len(acpAuthPrefix):])
}

// startACP starts the runtime. onURL gets each valid sign-in link once.
func startACP(cmd *exec.Cmd, onURL func(string)) (*acpClient, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &acpClient{cmd: cmd, stdin: stdin, pending: map[int]chan acpReply{}, exited: make(chan struct{})}

	var once sync.Once
	emit := func(line string) {
		if u, ok := authURLFromLine(line); ok && onURL != nil {
			once.Do(func() { onURL(u) })
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r := bufio.NewReaderSize(stderr, 64*1024)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				emit(line)
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		r := bufio.NewReaderSize(stdout, 64*1024)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				c.handleLine(strings.TrimSpace(line), emit)
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		_ = cmd.Wait()
		c.mu.Lock()
		c.closed = true
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.exited)
	}()
	return c, nil
}

func (c *acpClient) handleLine(line string, emit func(string)) {
	if line == "" {
		return
	}
	var msg struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Result json.RawMessage  `json:"result"`
		Error  *acpError        `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		emit(line)
		return
	}
	if msg.ID == nil {
		return
	}
	if msg.Method != "" {
		// A request from the runtime. Tori offers no capabilities.
		reply, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": msg.ID,
			"error": map[string]any{"code": -32601, "message": "not supported"},
		})
		c.mu.Lock()
		if !c.closed {
			_, _ = c.stdin.Write(append(reply, '\n'))
		}
		c.mu.Unlock()
		return
	}
	var id int
	if json.Unmarshal(*msg.ID, &id) != nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- acpReply{result: msg.Result, err: msg.Error}
	}
}

// call sends one request and waits for its answer.
func (c *acpClient) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errACPExited
	}
	c.nextID++
	id := c.nextID
	ch := make(chan acpReply, 1)
	c.pending[id] = ch
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err == nil {
		_, err = c.stdin.Write(append(body, '\n'))
	}
	c.mu.Unlock()
	if err != nil {
		return nil, errACPExited
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case rep, ok := <-ch:
		if !ok {
			return nil, errACPExited
		}
		if rep.err != nil {
			return nil, rep.err
		}
		return rep.result, nil
	case <-ctx.Done():
		return nil, errACPCancelled
	case <-timer.C:
		return nil, errACPTimeout
	case <-c.exited:
		return nil, errACPExited
	}
}

// stop closes stdin and ends the runtime: terminate, then kill after 2 seconds.
func (c *acpClient) stop() {
	_ = c.stdin.Close()
	select {
	case <-c.exited:
		return
	default:
	}
	terminateProc(c.cmd)
	select {
	case <-c.exited:
	case <-time.After(2 * time.Second):
		killProc(c.cmd)
		select {
		case <-c.exited:
		case <-time.After(2 * time.Second):
		}
	}
}
