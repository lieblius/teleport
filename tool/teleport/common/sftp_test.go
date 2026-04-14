// Teleport
// Copyright (C) 2026 Gravitational, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sftputils "github.com/gravitational/teleport/lib/sshutils/sftp"
)

func TestEvalSymlinks(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	tempRoot, err := filepath.EvalSymlinks(tempDir)
	require.NoError(t, err)
	subDir := filepath.Join(tempRoot, "foo")
	require.NoError(t, os.Mkdir(subDir, 0o700))
	realFile := filepath.Join(subDir, "bar.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("test data"), 0o600))
	nonexistentFile := filepath.Join(subDir, "idontexist.txt")

	dirLink := filepath.Join(tempRoot, "dirLink")
	require.NoError(t, os.Symlink(subDir, dirLink))
	fileLink := filepath.Join(tempRoot, "filelink")
	require.NoError(t, os.Symlink(realFile, fileLink))
	absLinkToNonexistentFile := filepath.Join(tempRoot, "abs")
	require.NoError(t, os.Symlink(nonexistentFile, absLinkToNonexistentFile))
	relLinkToNonexistentFile := filepath.Join(tempRoot, "rel")
	require.NoError(t, os.Symlink("foo/idontexist.txt", relLinkToNonexistentFile))

	tests := []struct {
		name         string
		path         string
		expectedPath string
	}{
		{
			name:         "real path",
			path:         realFile,
			expectedPath: realFile,
		},
		{
			name:         "real path to nonexistent file",
			path:         nonexistentFile,
			expectedPath: nonexistentFile,
		},
		{
			name:         "symlink in parent",
			path:         filepath.Join(dirLink, "bar.txt"),
			expectedPath: realFile,
		},
		{
			name:         "link to file",
			path:         fileLink,
			expectedPath: realFile,
		},
		{
			name:         "absolute link to nonexistent file",
			path:         absLinkToNonexistentFile,
			expectedPath: nonexistentFile,
		},
		{
			name:         "relative link to nonexistent file",
			path:         relLinkToNonexistentFile,
			expectedPath: nonexistentFile,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := evalSymlinks(tc.path)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedPath, out)
		})
	}

	t.Run("don't eval with bad path", func(t *testing.T) {
		_, err := evalSymlinks(filepath.Join(tempRoot, "this/does/not/exist.txt"))
		require.Error(t, err)
	})
}

func TestEnsureReqIsAllowed(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	tempRoot, err := filepath.EvalSymlinks(tempDir)
	require.NoError(t, err)
	file := filepath.Join(tempRoot, "foo.txt")
	require.NoError(t, os.WriteFile(file, []byte("foo"), 0o600))
	link := filepath.Join(tempRoot, "link")
	require.NoError(t, os.Symlink(file, link))

	passTests := []struct {
		name    string
		allowed *allowedOps
		req     *sftp.Request
	}{
		{
			name: "no restrictions",
			req: &sftp.Request{
				Filepath: "/foo/bar/baz.txt",
				Method:   sftputils.MethodGet,
			},
		},
		{
			name:    "read",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodGet,
			},
		},
		{
			name:    "write",
			allowed: &allowedOps{path: file, write: true},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodPut,
			},
		},
		{
			name:    "chmod",
			allowed: &allowedOps{path: file, write: true},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodSetStat,
			},
		},
		{
			name:    "stat in read mode",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodStat,
			},
		},
		{
			name:    "lstat in read mode",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodLstat,
			},
		},
		{
			name:    "stat in write mode",
			allowed: &allowedOps{path: file, write: true},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodStat,
			},
		},
		{
			name:    "lstat in write mode",
			allowed: &allowedOps{path: file, write: true},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodLstat,
			},
		},
	}
	for _, tc := range passTests {
		t.Run("allow "+tc.name, func(t *testing.T) {
			handler := &sftpHandler{allowed: tc.allowed}
			require.NoError(t, handler.ensureReqIsAllowed(tc.req))
		})
	}

	convolutedPath := tempRoot + "/../" + filepath.Base(tempRoot) + "/foo.txt"
	failTests := []struct {
		name    string
		allowed *allowedOps
		req     *sftp.Request
	}{
		{
			name:    "uncleaned path",
			allowed: &allowedOps{path: convolutedPath},
			req: &sftp.Request{
				Filepath: convolutedPath,
				Method:   sftputils.MethodGet,
			},
		},
		{
			name:    "symlink",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: link,
				Method:   sftputils.MethodGet,
			},
		},
		{
			name:    "get in write mode",
			allowed: &allowedOps{path: file, write: true},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodGet,
			},
		},
		{
			name:    "write in read mode",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodPut,
			},
		},
		{
			name:    "chmod in read mode",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodSetStat,
			},
		},
		{
			name:    "unknown method",
			allowed: &allowedOps{path: file},
			req: &sftp.Request{
				Filepath: file,
				Method:   sftputils.MethodRename,
			},
		},
	}
	for _, tc := range failTests {
		t.Run("deny "+tc.name, func(t *testing.T) {
			handler := &sftpHandler{allowed: tc.allowed}
			require.Error(t, handler.ensureReqIsAllowed(tc.req))
		})
	}
}
