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
	"context"
	"errors"
)

type commandCall struct {
	name string
	args []string
}

type fakeCommandResponse struct {
	result commandResult
	err    error
}

type fakeCommandExecutor struct {
	calls     []commandCall
	responses []fakeCommandResponse
}

func (f *fakeCommandExecutor) Run(_ context.Context, name string, args ...string) (commandResult, error) {
	f.calls = append(f.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return commandResult{}, errors.New("unexpected command execution")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.result, response.err
}
