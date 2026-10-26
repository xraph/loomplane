package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"strings"
)

type FilesystemCapacity struct {
	TotalBytes int64 `json:"total_bytes"`
	FreeBytes  int64 `json:"free_bytes"`
}

// StorageCapacity samples the filesystem containing Docker's actual data root,
// through the same pinned SSH host as the verified daemon.
func (c *Client) StorageCapacity(ctx context.Context) (FilesystemCapacity, error) {
	var daemon struct{ ID, DockerRootDir string }
	if err := c.getJSON(ctx, "/info", &daemon); err != nil {
		return FilesystemCapacity{}, err
	}
	if daemon.ID == "" || c.Config == nil || (c.Config.Details["docker_id"] != "" && c.Config.Details["docker_id"] != daemon.ID) {
		return FilesystemCapacity{}, errors.New("the checked Docker daemon identity changed")
	}
	command, err := storageCapacityCommand(daemon.DockerRootDir)
	if err != nil {
		return FilesystemCapacity{}, err
	}
	if c.ssh == nil {
		return FilesystemCapacity{}, errors.New("filesystem observations require pinned SSH")
	}
	session, err := c.ssh.NewSession()
	if err != nil {
		return FilesystemCapacity{}, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	raw, err := session.Output(command)
	if err != nil {
		return FilesystemCapacity{}, errors.New("docker data filesystem capacity is unavailable")
	}
	return parseFilesystemCapacity(raw)
}
func parseFilesystemCapacity(raw []byte) (FilesystemCapacity, error) {
	var rawFacts struct {
		TotalBytes *int64 `json:"total_bytes"`
		FreeBytes  *int64 `json:"free_bytes"`
	}
	if err := json.Unmarshal(raw, &rawFacts); err != nil {
		return FilesystemCapacity{}, err
	}
	if rawFacts.TotalBytes == nil || rawFacts.FreeBytes == nil {
		return FilesystemCapacity{}, ErrInvalid
	}
	out := FilesystemCapacity{TotalBytes: *rawFacts.TotalBytes, FreeBytes: *rawFacts.FreeBytes}
	if out.TotalBytes <= 0 || out.FreeBytes < 0 || out.FreeBytes > out.TotalBytes {
		return FilesystemCapacity{}, ErrInvalid
	}
	return out, nil
}

func storageCapacityCommand(root string) (string, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsRune(root, 0) {
		return "", ErrInvalid
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(root))
	return "python3 - <<'PY'\n" + `import base64,glob,json,os,pathlib
root=base64.b64decode('` + encoded + `').decode('utf-8')
pids=[]
for f in glob.glob('/proc/[0-9]*/comm'):
 try:
  if pathlib.Path(f).read_text().strip()=='dockerd': pids.append(pathlib.Path(f).parent.name)
 except OSError: pass
assert len(pids)==1, 'exact local Docker daemon required'
# Resolve inside the daemon's mount namespace. An identically named path in
# the SSH user's namespace can live on a different filesystem.
data='/proc/'+pids[0]+'/root'+root
s=os.statvfs(data)
print(json.dumps({'total_bytes':s.f_blocks*s.f_frsize,'free_bytes':s.f_bavail*s.f_frsize}))
PY`, nil
}
