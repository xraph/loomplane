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
import glob,json,os,pathlib,re
root=pathlib.Path('/sys/fs/cgroup')
available=set((root/'cgroup.controllers').read_text().split())
assert {'cpu','memory','cpuset'} <= available, 'required cgroup v2 controllers unavailable'
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
 assert entry.startswith('/') and '..' not in pathlib.PurePosixPath(entry).parts
 current=root/entry.lstrip('/')
 assert current.is_dir(), 'hidden cgroup hierarchy'
 while True:
  paths.append(current)
  if current==root: break
  current=current.parent
# Controller files belong to the parent's enabled subtree. A missing file
# inherits only when the readable parent configuration proves it is disabled.
def limit_file(p,controller,name):
 file=p/name
 if file.exists(): return file.read_text().strip()
 if p==root:
  assert controller!='cpuset', 'root effective CPU set unavailable'
  return None
 enabled=set((p.parent/'cgroup.subtree_control').read_text().split())
 assert enabled <= available, 'unknown enabled controller'
 assert controller not in enabled, 'enabled controller limit unavailable'
 return None
cpu=2**60;memory=2**60
for p in paths:
 value=limit_file(p,'cpu','cpu.max')
 if value is not None:
  quota,period=value.split()
  assert period.isdigit() and int(period)>0, 'invalid CPU period'
  if quota!='max':
   assert quota.isdigit() and int(quota)>0, 'invalid CPU quota'
   cpu=min(cpu,int(quota)*1000//int(period))
 value=limit_file(p,'memory','memory.max')
 if value is not None and value!='max':
  assert value.isdigit() and int(value)>0, 'invalid memory limit'
  memory=min(memory,int(value))
 cpuset=limit_file(p,'cpuset','cpuset.cpus.effective')
 if cpuset is not None:
  assert cpuset, 'unknown effective CPU set'
  count=0;previous=-1
  for part in cpuset.split(','):
   assert re.fullmatch(r'[0-9]+(?:-[0-9]+)?',part), 'invalid CPU set'
   bounds=part.split('-');low=int(bounds[0]);high=int(bounds[-1])
   assert high>=low and low>previous, 'invalid CPU range'
   count+=high-low+1;previous=high
  cpu=min(cpu,count*1000)
print(json.dumps({'cpu':cpu,'memory':memory}))
PY`
