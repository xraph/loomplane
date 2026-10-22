package server

import (
	"context"
	"encoding/json"
	"errors"
)

// EffectiveCapacity bounds daemon facts by the daemon's visible cgroup limits.
// Unknown layouts fail closed so callers cannot reserve the outer host budget.
func (c *Client) EffectiveCapacity(ctx context.Context, cpus int, memory int64) (cpuMillis, memoryBytes int64, err error) {
	session, err := c.ssh.NewSession()
	if err != nil {
		return 0, 0, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	out, err := session.Output(effectiveCapacityCommand)
	if err != nil {
		return 0, 0, errors.New("effective Docker cgroup limits are unavailable")
	}
	var limits struct {
		CPU    int64 `json:"cpu"`
		Memory int64 `json:"memory"`
	}
	if checkErr := json.Unmarshal(out, &limits); checkErr != nil {
		return 0, 0, checkErr
	}
	cpu := min(int64(cpus)*1000, limits.CPU)
	mem := min(memory, limits.Memory)
	if cpu <= 0 || mem <= 0 {
		return 0, 0, errors.New("effective Docker capacity is invalid")
	}
	return cpu, mem, nil
}

const effectiveCapacityCommand = `python3 - <<'PY'
import glob,json,os,pathlib
root=pathlib.Path('/sys/fs/cgroup')
assert (root/'cgroup.controllers').exists(), 'cgroup v2 required'
pids=[]
for f in glob.glob('/proc/[0-9]*/comm'):
 try:
  if pathlib.Path(f).read_text().strip()=='dockerd': pids.append(f.split('/')[2])
 except OSError: pass
assert len(pids)==1, 'exact local Docker daemon required'
paths=[]
for pid in [pids[0],str(os.getpid())]:
 entries=pathlib.Path('/proc/'+pid+'/cgroup').read_text().splitlines()
 entry=next(x[3:] for x in entries if x.startswith('0::'))
 assert '..' not in pathlib.PurePosixPath(entry).parts
 current=root/entry.lstrip('/')
 assert current.is_dir(), 'hidden cgroup hierarchy'
 while True:
  paths.append(current)
  if current==root: break
  current=current.parent
cpu=2**60;memory=2**60
for p in paths:
 assert (p/'cpu.max').exists() or p==root
 quota,period=(p/'cpu.max').read_text().split() if (p/'cpu.max').exists() else ['max','100000']
 if quota!='max': cpu=min(cpu,int(quota)*1000//int(period))
 assert (p/'memory.max').exists() or p==root
 limit=(p/'memory.max').read_text().strip() if (p/'memory.max').exists() else 'max'
 if limit!='max': memory=min(memory,int(limit))
 cpuset=(p/'cpuset.cpus.effective').read_text().strip()
 assert cpuset, 'unknown effective CPU set'
 count=0
 for part in cpuset.split(','):
  bounds=part.split('-');count+=int(bounds[-1])-int(bounds[0])+1
 cpu=min(cpu,count*1000)
print(json.dumps({'cpu':cpu,'memory':memory}))
PY`
