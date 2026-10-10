// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceActions_jobParser(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		workflow        string
		singleWorkflows []string
	}{
		{
			name: "OneJobNoDuplicate",
			workflow: `
jobs:
  job1:
    runs-on: docker
    steps:
      - run: echo OK
`,
			singleWorkflows: []string{
				`jobs:
    job1:
        name: job1
        runs-on: docker
        steps:
            - run: echo OK
`,
			},
		},
		{
			name: "MatrixTwoJobsWithSameJobName",
			workflow: `
name: test
jobs:
  job1:
    name: shadowdefaultmatrixgeneratednames
    strategy:
      matrix:
        version: [1.17, 1.19]
    runs-on: docker
    steps:
      - run: echo OK
`,
			singleWorkflows: []string{
				`name: test
jobs:
    job1:
        name: shadowdefaultmatrixgeneratednames
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.17
`,
				`name: test
jobs:
    job1:
        name: shadowdefaultmatrixgeneratednames-1
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.19
`,
			},
		},
		{
			name: "MatrixTwoJobsWithMatrixGeneratedNames",
			workflow: `
name: test
jobs:
  job1:
    strategy:
      matrix:
        version: [1.17, 1.19]
    runs-on: docker
    steps:
      - run: echo OK
`,
			singleWorkflows: []string{
				`name: test
jobs:
    job1:
        name: job1 (1.17)
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.17
`,
				`name: test
jobs:
    job1:
        name: job1 (1.19)
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.19
`,
			},
		},
		{
			name: "MatrixTwoJobsWithDistinctInterpolatedNames",
			workflow: `
name: test
jobs:
  job1:
    name: myname-${{ matrix.version }}
    strategy:
      matrix:
        version: [1.17, 1.19]
    runs-on: docker
    steps:
      - run: echo OK
`,
			singleWorkflows: []string{
				`name: test
jobs:
    job1:
        name: myname-1.17
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.17
`,
				`name: test
jobs:
    job1:
        name: myname-1.19
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.19
`,
			},
		},
		{
			name: "MatrixTwoJobsWithIdenticalInterpolatedNames",
			workflow: `
name: test
jobs:
  job1:
    name: myname-${{ matrix.typo }}
    strategy:
      matrix:
        version: [1.17, 1.19]
    runs-on: docker
    steps:
      - run: echo OK
`,
			singleWorkflows: []string{
				`name: test
jobs:
    job1:
        name: myname-
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.17
`,
				`name: test
jobs:
    job1:
        name: myname--1
        runs-on: docker
        steps:
            - run: echo OK
        strategy:
            matrix:
                version:
                    - 1.19
`,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sw, err := JobParser([]byte(testCase.workflow))
			require.NoError(t, err)
			for i, sw := range sw {
				actual, err := sw.Marshal()
				require.NoError(t, err)
				assert.Equal(t, testCase.singleWorkflows[i], string(actual))
			}
		})
	}
}

func TestRejectWorkflowMetadata(t *testing.T) {
	for content, refused := range map[string]bool{
		"jobs: {j: {runs-on: x}}": false,
		"jobs: {j: {runs-on: x, __metadata: {workflow_call_parent: a}}}":                 false,
		"__metadata: {workflow_call_parent: a}\njobs: {j: {runs-on: x}}":                 true,
		"__metadata:\njobs: {j: {runs-on: x}}":                                           true,
		"\"__metadata\": {workflow_call_id: a}\njobs: {j: {runs-on: x}}":                 true,
		"x: &m {__metadata: {workflow_call_parent: a}}\n<<: *m\njobs: {j: {runs-on: x}}": true,
	} {
		err := RejectWorkflowMetadata([]byte(content))
		if refused {
			require.Error(t, err, content)
		} else {
			require.NoError(t, err, content)
		}
		_, err = JobParser([]byte(content))
		if refused {
			require.ErrorContains(t, err, "__metadata", content)
		}
	}
}
