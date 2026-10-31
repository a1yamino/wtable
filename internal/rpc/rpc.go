package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const MaxFrame = 8 << 20

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Client struct {
	conn net.Conn
	scan *bufio.Scanner
	mu   sync.Mutex
	id   uint64
}

func Dial(ctx context.Context, socket, token string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, scan: bufio.NewScanner(conn)}
	c.scan.Buffer(make([]byte, 4096), MaxFrame)
	if err = c.Call(ctx, "auth", map[string]string{"token": token}, nil); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Call(ctx context.Context, method string, args, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// The implicit deadline is a safety net for short calls, not a contract for
	// blocking ones. Callers that legitimately wait longer (task.next/wait,
	// ACP restart, git) must pass a context with their own deadline.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return err
	}
	params, err := json.Marshal(args)
	if err != nil {
		return err
	}
	c.id++
	data, err := json.Marshal(Request{JSONRPC: "2.0", ID: c.id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if len(data) >= MaxFrame {
		return errors.New("request exceeds frame limit")
	}
	if _, err = c.conn.Write(append(data, '\n')); err != nil {
		return err
	}
	if !c.scan.Scan() {
		if err = c.scan.Err(); err != nil {
			return err
		}
		return errors.New("daemon disconnected")
	}
	var res Response
	if err = json.Unmarshal(c.scan.Bytes(), &res); err != nil {
		return err
	}
	if res.JSONRPC != "2.0" || res.ID != c.id {
		return fmt.Errorf("invalid RPC response for request %d", c.id)
	}
	if res.Error != nil {
		return res.Error
	}
	if result != nil {
		return json.Unmarshal(res.Result, result)
	}
	return nil
}
