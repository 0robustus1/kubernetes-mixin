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

import (
	"bytes"
	"context"
	"os/exec"
)

const (
	maxCommandStdoutBytes = 32 << 20
	maxCommandStderrBytes = 1 << 20
)

type commandResult struct {
	stdout          []byte
	stderr          []byte
	stdoutTruncated bool
	stderrTruncated bool
}

type commandExecutor interface {
	Run(ctx context.Context, name string, args ...string) (commandResult, error)
}

type osCommandExecutor struct{}

func (osCommandExecutor) Run(ctx context.Context, name string, args ...string) (commandResult, error) {
	stdout := newLimitedBuffer(maxCommandStdoutBytes)
	stderr := newLimitedBuffer(maxCommandStderrBytes)
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	return commandResult{
		stdout:          stdout.Bytes(),
		stderr:          stderr.Bytes(),
		stdoutTruncated: stdout.truncated,
		stderrTruncated: stderr.truncated,
	}, err
}

type limitedBuffer struct {
	bytes.Buffer
	remaining int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	if len(value) > b.remaining {
		value = value[:b.remaining]
		b.truncated = true
	}
	if len(value) > 0 {
		_, _ = b.Buffer.Write(value)
		b.remaining -= len(value)
	}
	return originalLength, nil
}
