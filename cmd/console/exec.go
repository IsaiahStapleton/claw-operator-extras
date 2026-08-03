/*
Copyright 2026 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Reading files out of a Claw pod through the Kubernetes exec API.
//
// Claw home volumes are ReadWriteOnce, so a single console pod cannot mount
// the volumes of every Claw it reports on — exec is how one console reaches
// many Claws across many namespaces. The request carries the user's own
// forwarded token, so a user who cannot exec in a namespace cannot read that
// namespace's agents either.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

// Channel prefixes of the v4.channel.k8s.io framing: the API server tags each
// binary message with the stream it came from.
const (
	streamStdout = 1
	streamStderr = 2
	streamError  = 3
)

// execLimits bound what a single exec may return, so a pathological session
// file cannot exhaust the console's memory.
const (
	execMaxOutputBytes = 64 << 20 // 64 MiB
)

// execResult is the captured output of one command.
type execResult struct {
	Stdout []byte
	Stderr []byte
}

// execInPod runs argv in a container and returns its output. It never writes
// to the pod: callers pass read-only commands, and no stdin is attached.
func (s *server) execInPod(ctx context.Context, identity userIdentity, namespace, pod, container string, argv []string) (*execResult, error) {
	q := url.Values{}
	q.Set("container", container)
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	for _, a := range argv {
		q.Add("command", a)
	}

	endpoint := strings.Replace(s.apiServer, "https://", "wss://", 1) +
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec?%s",
			url.PathEscape(namespace), url.PathEscape(pod), q.Encode())

	header := http.Header{}
	req := &http.Request{Header: header}
	s.setAuth(req, identity)

	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient:   s.client,
		HTTPHeader:   header,
		Subprotocols: []string{"v4.channel.k8s.io"},
	})
	if err != nil {
		// A rejected upgrade carries the API server's status, which is how an
		// authorization failure arrives here. Surface it rather than a generic
		// connection error so the user is told they lack access.
		if resp != nil {
			return nil, apiError{StatusCode: resp.StatusCode,
				Message: fmt.Sprintf("exec into %s/%s denied or unavailable: %s", namespace, pod, resp.Status)}
		}
		return nil, fmt.Errorf("exec into %s/%s: %w", namespace, pod, err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	// Session files run to megabytes; the library defaults to a 32 KiB cap.
	conn.SetReadLimit(execMaxOutputBytes)

	var stdout, stderr bytes.Buffer
	var execErr error
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// A normal command completion closes the stream; that is not a
			// failure. Anything the context aborted is.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Only a clean close means the output is complete. An abnormal
			// close — the read limit tripped, the connection dropped —
			// truncates the file, and handing that back as success is exactly
			// the silent omission this console exists to refuse.
			if !normalClose(err) {
				return nil, fmt.Errorf("exec stream from %s/%s ended abnormally: %w", namespace, pod, err)
			}
			break
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case streamStdout:
			stdout.Write(data[1:])
		case streamStderr:
			stderr.Write(data[1:])
		case streamError:
			execErr = execStatusError(data[1:])
		}
		if stdout.Len()+stderr.Len() > execMaxOutputBytes {
			return nil, fmt.Errorf("exec output from %s/%s exceeded %d bytes", namespace, pod, execMaxOutputBytes)
		}
	}
	if execErr != nil {
		return nil, execErr
	}
	return &execResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

// execStream runs argv and hands stdout to fn as a stream. Batched file reads
// can run to hundreds of megabytes, which is far past what execInPod will hold
// in memory — streaming lets the caller consume as bytes arrive.
func (s *server) execStream(ctx context.Context, identity userIdentity, namespace, pod, container string, argv []string, fn func(io.Reader) error) error {
	q := url.Values{}
	q.Set("container", container)
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	for _, a := range argv {
		q.Add("command", a)
	}

	endpoint := strings.Replace(s.apiServer, "https://", "wss://", 1) +
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec?%s",
			url.PathEscape(namespace), url.PathEscape(pod), q.Encode())

	header := http.Header{}
	s.setAuth(&http.Request{Header: header}, identity)

	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient:   s.client,
		HTTPHeader:   header,
		Subprotocols: []string{"v4.channel.k8s.io"},
	})
	if err != nil {
		if resp != nil {
			return apiError{StatusCode: resp.StatusCode,
				Message: fmt.Sprintf("exec into %s/%s denied or unavailable: %s", namespace, pod, resp.Status)}
		}
		return fmt.Errorf("exec into %s/%s: %w", namespace, pod, err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	conn.SetReadLimit(execMaxOutputBytes)

	pr, pw := io.Pipe()
	go func() {
		var execErr error
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				// An abnormal close means the stream this pipe is carrying was
				// cut short; propagate that to fn as an error rather than a
				// clean EOF, so a truncated tar is not parsed as complete.
				if ctx.Err() != nil {
					execErr = ctx.Err()
				} else if !normalClose(err) {
					execErr = fmt.Errorf("exec stream from %s/%s ended abnormally: %w", namespace, pod, err)
				}
				break
			}
			if len(data) == 0 {
				continue
			}
			switch data[0] {
			case streamStdout:
				if _, werr := pw.Write(data[1:]); werr != nil {
					// The consumer stopped early (it had all it needed);
					// closing the pipe unblocks this goroutine.
					_ = pw.CloseWithError(werr)
					return
				}
			case streamError:
				execErr = execStatusError(data[1:])
			}
		}
		_ = pw.CloseWithError(execErr)
	}()

	err = fn(pr)
	_ = pr.CloseWithError(err)
	return err
}

// normalClose reports whether a read error is the clean end of a stream the
// API server closed after the command finished, as opposed to a truncation.
// A read-limit trip closes with StatusMessageTooBig, a dropped connection
// carries no close code at all; both must read as abnormal so the caller
// rejects the shortened output rather than accepting it.
func normalClose(err error) bool {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return true
	default:
		return false
	}
}

// execStatusError decodes the terminal status the API server writes to the
// error stream. Success is reported there too, so only a failure returns err.
func execStatusError(payload []byte) error {
	var status struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &status); err != nil {
		return nil // unparseable status is not itself proof of failure
	}
	if status.Status == "" || strings.EqualFold(status.Status, "Success") {
		return nil
	}
	msg := status.Message
	if msg == "" {
		msg = status.Reason
	}
	return fmt.Errorf("command failed in container: %s", msg)
}
