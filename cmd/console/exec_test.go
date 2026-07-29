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

package main

import (
	"errors"
	"testing"

	"github.com/coder/websocket"
)

// A truncated read must not read as success: only a clean close means the pod
// output is complete, and a read-limit trip or dropped connection is a
// truncation the console has to reject rather than parse as a whole file.
func TestNormalClose(t *testing.T) {
	if normalClose(errors.New("connection reset by peer")) {
		t.Error("a non-close error is abnormal")
	}
	if normalClose(websocket.CloseError{Code: websocket.StatusMessageTooBig}) {
		t.Error("read-limit close (StatusMessageTooBig) is abnormal")
	}
	if normalClose(websocket.CloseError{Code: websocket.StatusAbnormalClosure}) {
		t.Error("abnormal closure is abnormal")
	}
	if !normalClose(websocket.CloseError{Code: websocket.StatusNormalClosure}) {
		t.Error("normal closure is the clean end of a stream")
	}
	if !normalClose(websocket.CloseError{Code: websocket.StatusGoingAway}) {
		t.Error("going-away is a clean close")
	}
}
