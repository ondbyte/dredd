package firecracker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/ondbyte/dredd/agent"
)

// VsockHostDial connects to the guest's vsock listener via the host-side UDS
// produced by Firecracker. Firecracker exposes vsock through a UNIX socket;
// the caller sends "CONNECT <port>\n" then reads "OK <peer_port>\n" before
// switching to data mode.
func VsockHostDial(socketPath string, port uint32, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var conn net.Conn
	var err error
	for {
		conn, err = net.DialTimeout("unix", socketPath, time.Until(deadline))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("vsock dial %s: %w", socketPath, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, err
	}
	buf := make([]byte, 0, 32)
	one := make([]byte, 1)
	for {
		n, rerr := conn.Read(one)
		if rerr != nil {
			conn.Close()
			return nil, fmt.Errorf("vsock handshake read: %w", rerr)
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if one[0] == '\n' {
			break
		}
		if len(buf) > 64 {
			conn.Close()
			return nil, errors.New("vsock handshake reply too long")
		}
	}
	if len(buf) < 3 || string(buf[:2]) != "OK" {
		conn.Close()
		return nil, fmt.Errorf("vsock handshake rejected: %q", string(buf))
	}
	// Clear deadline; caller will set its own deadlines around Exchange.
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// Exchange writes one length-prefixed JSON ExecRequest and reads one
// length-prefixed JSON ExecResponse on the given connection.
func Exchange(conn net.Conn, req *agent.ExecRequest, timeout time.Duration) (*agent.ExecResponse, error) {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(payload) > agent.MaxFrameBytes {
		return nil, fmt.Errorf("request frame too large: %d", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write payload: %w", err)
	}

	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > agent.MaxFrameBytes {
		return nil, fmt.Errorf("invalid response frame size: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	var resp agent.ExecResponse
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &resp, nil
}
