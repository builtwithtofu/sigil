package decorator

import (
	"context"
	"io/fs"
	"os"
)

func openFileForOutput(_ context.Context, path string, flags int, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}
