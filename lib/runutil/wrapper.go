package runutil

// TODO(jsimsa): Write wrappers for functions from the Go standard
// libraries "os" and "ioutil" that our tools use: Chmod(), Create(),
// Open(), OpenFile(), ...

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"syscall"
)

// helper executes the given Go standard library function,
// encapsulated as a closure, respecting the "dry run" option.
func (r *Run) helper(fn func() error, format string, args ...interface{}) error {
	opts := r.opts
	if opts.DryRun {
		opts.Verbose = true
		return r.FunctionWithOpts(opts, func() error { return nil }, format, args...)
	}
	return r.Function(fn, format, args...)
}

// Chdir is a wrapper around os.Chdir that handles options such as
// "verbose" or "dry run".
func (r *Run) Chdir(dir string) error {
	opts := r.opts
	if opts.DryRun {
		// Disable the dry run option as this function has no
		// effect and doing so results in more informative
		// "dry run" output.
		opts.DryRun = false
		opts.Verbose = true
	}
	return r.FunctionWithOpts(opts, func() error { return os.Chdir(dir) }, fmt.Sprintf("cd %q", dir))
}

// MkdirAll is a wrapper around os.MkdirAll that handles options such
// as "verbose" or "dry run".
func (r *Run) MkdirAll(dir string, mode os.FileMode) error {
	return r.helper(func() error { return os.MkdirAll(dir, mode) }, fmt.Sprintf("mkdir -p %q", dir))
}

// RemoveAll is a wrapper around os.RemoveAll that handles options
// such as "verbose" or "dry run".
func (r *Run) RemoveAll(dir string) error {
	return r.helper(func() error { return os.RemoveAll(dir) }, fmt.Sprintf("rm -rf %q", dir))
}

// Rename is a wrapper around os.Rename that handles options such as
// "verbose" or "dry run".
func (r *Run) Rename(src, dst string) error {
	return r.helper(func() error {
		if err := os.Rename(src, dst); err != nil {
			// Check if the rename operation failed
			// because the source and destination are
			// located on different mount points.
			linkErr, ok := err.(*os.LinkError)
			if !ok {
				return err
			}
			errno, ok := linkErr.Err.(syscall.Errno)
			if !ok || errno != syscall.EXDEV {
				return err
			}
			// Fall back to a non-atomic rename.
			cmd := exec.Command("mv", src, dst)
			return cmd.Run()
		}
		return nil
	}, fmt.Sprintf("mv %q %q", src, dst))
}

// Symlink is a wrapper around os.Symlink that handles options such as
// "verbose" or "dry run".
func (r *Run) Symlink(src, dst string) error {
	return r.helper(func() error { return os.Symlink(src, dst) }, fmt.Sprintf("ln -s %q %q", src, dst))
}

// TempDir is a wrapper around ioutil.TempDir that handles options
// such as "verbose" or "dry run".
func (r *Run) TempDir(dir, prefix string) (string, error) {
	tmpDir := fmt.Sprintf("%v%c%vXXXXXX", dir, os.PathSeparator, prefix)
	var err error
	if dir == "" {
		dir = os.Getenv("TMPDIR")
	}
	r.helper(func() error {
		tmpDir, err = ioutil.TempDir(dir, prefix)
		return err
	}, fmt.Sprintf("mkdir -p %q", tmpDir))
	return tmpDir, err
}

// WriteFile is a wrapper around ioutil.WriteFile that handles options
// such as "verbose" or "dry run".
func (r *Run) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return r.helper(func() error { return ioutil.WriteFile(filename, data, perm) }, fmt.Sprintf("write %q", filename))
}
