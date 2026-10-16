// Copyright kubernetes-mixin Authors
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "testing"

func TestLimitedBufferBoundsCapturedOutput(t *testing.T) {
	t.Parallel()

	buffer := newLimitedBuffer(4)
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if written != 6 {
		t.Fatalf("Write() = %d, want 6", written)
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("captured output = %q", got)
	}
	if !buffer.truncated {
		t.Fatal("buffer did not report truncation")
	}

	written, err = buffer.Write([]byte("more"))
	if err != nil || written != 4 || buffer.String() != "abcd" {
		t.Fatalf("second Write() = %d, %v, output %q", written, err, buffer.String())
	}
}
