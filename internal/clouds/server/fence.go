package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type EffectFence struct {
	Operation  string `json:"operation"`
	Generation int64  `json:"generation"`
}
type effectFenceKey struct{}

func WithEffectFence(ctx context.Context, f EffectFence) context.Context {
	return context.WithValue(ctx, effectFenceKey{}, f)
}
func EffectFenceFrom(ctx context.Context) (EffectFence, bool) {
	f, ok := ctx.Value(effectFenceKey{}).(EffectFence)
	return f, ok && f.Operation != "" && f.Generation > 0
}

// ArmEffectFence drains the daemon's previous synchronous mutation and records
// this operation's generation. A dirty journal blocks takeover after an unknown
// helper failure; lock absence alone is never a quiescence receipt.
func (c *Client) ArmEffectFence(ctx context.Context, f EffectFence) error {
	response, err := c.fencedRequest(ctx, "advance", f, http.MethodPost, "/", nil, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("runtime effect fence could not establish quiescence")
	}
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

type fenceBody struct {
	io.ReadCloser
	closeSession func() error
	stop         func() bool
}

func (b *fenceBody) Close() error {
	b.stop()
	return errors.Join(b.ReadCloser.Close(), b.closeSession())
}
func (c *Client) fencedRequest(ctx context.Context, mode string, f EffectFence, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	if c.ssh == nil || c.Config.Details["docker_id"] == "" {
		return nil, errors.New("verified SSH runtime fencing is unavailable")
	}
	session, err := c.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	// The constant script contains no credentials. Request headers and bodies use
	// stdin only and never enter a shell command, file log or error response.
	if err = session.Start("python3 -c '" + strings.ReplaceAll(effectFenceScript, "'", "'\"'\"'") + "'"); err != nil {
		_ = session.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	request := struct {
		Mode, Daemon, Socket, Method, Path string
		Fence                              EffectFence
		Headers                            http.Header
	}{mode, c.Config.Details["docker_id"], c.Config.Socket, method, path, f, headers}
	if err = json.NewEncoder(input).Encode(request); err == nil && body != nil {
		_, err = io.Copy(input, body)
	}
	closeErr := input.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		stop()
		_ = session.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(output), nil)
	if err != nil {
		stop()
		_ = session.Close()
		return nil, errors.New("runtime effect fence outcome is unknown")
	}
	response.Body = &fenceBody{ReadCloser: response.Body, closeSession: session.Close, stop: stop}
	return response, nil
}

const effectFenceScript = `import os,sys,json,hashlib,fcntl,socket,http.client,signal,tempfile,stat,re,time,struct
# Once the complete request has arrived, caller loss must not interrupt the
# daemon request. An uncatchable termination leaves the durable dirty marker.
for sig in (signal.SIGHUP,signal.SIGTERM,signal.SIGINT): signal.signal(sig,signal.SIG_IGN)
out=sys.stdout.buffer
alive=True
responding=False
def emit(data):
 global alive
 if alive:
  try: out.write(data);out.flush()
  except (BrokenPipeError,OSError): alive=False
def fail():
 if responding: return
 emit(b'HTTP/1.1 409 Conflict\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
def main():
 global responding
 q=json.loads(sys.stdin.buffer.readline(65536))
 assert q['Fence']['generation']>0 and q['Fence']['operation'] and q['Daemon']
 payload=tempfile.TemporaryFile()
 while True:
  chunk=sys.stdin.buffer.read(65536)
  if not chunk: break
  payload.write(chunk)
 size=payload.tell();payload.seek(0)
 root='/var/tmp/loomplane-fence-'+hashlib.sha256(q['Daemon'].encode()).hexdigest()
 try: os.mkdir(root,0o700)
 except FileExistsError: pass
 info=os.lstat(root)
 assert stat.S_ISDIR(info.st_mode) and info.st_uid==os.geteuid() and stat.S_IMODE(info.st_mode)==0o700
 directory=os.open(root,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 lock=os.open('daemon.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600,dir_fd=directory)
 fcntl.flock(lock,fcntl.LOCK_EX)
 # One daemon-wide journal prevents a killed helper for any operation from
 # silently leaving an in-flight Docker mutation behind during another claim.
 journal=os.open('state',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600,dir_fd=directory)
 raw=os.read(journal,16*1024*1024)
 state=json.loads(raw) if raw else {'epochs':{},'dirty':False}
 assert not state['dirty']
 def save():
  data=json.dumps(state).encode();os.lseek(journal,0,os.SEEK_SET);os.write(journal,data);os.ftruncate(journal,len(data));os.fsync(journal);os.fsync(directory)
 class UnixHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.connect(q['Socket'])
 c=UnixHTTP('docker')
 c.request('GET','/info');r=c.getresponse();identity=json.loads(r.read())
 assert r.status==200 and identity['ID']==q['Daemon']
 op=hashlib.sha256(q['Fence']['operation'].encode()).hexdigest();generation=q['Fence']['generation'];old=state['epochs'].get(op,0)
 if q['Mode']=='advance':
  assert generation>=old
  state['epochs'][op]=generation;save()
  emit(b'HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n');return
 assert generation==old and old>0
 ingress=q['Mode']=='ingress-exec'
 if ingress:
  assert q['Method']=='POST' and re.fullmatch(r'/exec/[a-f0-9]{64}/start',q['Path'])
  assert json.loads(payload.read())=={'Detach':False,'Tty':False};payload.seek(0)
  inspect=q['Path'][:-5]+'json'
  c.request('GET',inspect);r=c.getresponse();execution=json.loads(r.read());assert r.status==200
  container=execution['ContainerID'];assert re.fullmatch(r'[a-f0-9]{64}',container)
  assert not execution['Running']
  process=execution['ProcessConfig'];args=process['arguments']
  assert not process['tty'] and not process['privileged'] and process['entrypoint']=='sh' and args[0]=='-c'
  admin=r"""` + ingressAdminScript + `"""
  probe=r"""` + ingressProbeScript + `"""
  assert (len(args)==6 and args[1]==admin and args[2]=='ingress' and args[3] in ('read','apply')) or (len(args)==4 and args[1]==probe and args[2]=='probe' and re.fullmatch(r'[a-z0-9.-]+',args[3]))
  c.request('GET','/containers/'+container+'/json');r=c.getresponse();owned=json.loads(r.read());assert r.status==200
  assert owned['Id']==container and owned['State']['Running'] and owned['Config']['Image']=='` + IngressImage + `'
  assert owned['Config']['Labels'].get('loomplane.org') and owned['Config']['Labels'].get('loomplane.ingress')
  assert q['Fence']['operation']==owned['Config']['Labels']['loomplane.org']+'/ingress/'+owned['Config']['Labels']['loomplane.ingress']
 headers={k:','.join(v) for k,v in (q['Headers'] or {}).items()};headers['Content-Length']=str(size)
 state['dirty']=True;save()
 c.request(q['Method'],q['Path'],body=payload,headers=headers);r=c.getresponse()
 assert r.chunked or r.length is not None or (ingress and r.status==200)
 responding=True
 emit(('HTTP/1.1 '+str(r.status)+' '+r.reason+'\r\n').encode())
 for k,v in r.getheaders():
  if k.lower() not in ('transfer-encoding','connection','content-length'): emit((k+': '+v+'\r\n').encode())
 emit(b'Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n')
 def output(chunk):
  emit(('%x\r\n'%len(chunk)).encode()+chunk+b'\r\n')
 if ingress and r.status==200:
  # Docker's synchronous non-TTY exec uses a close-delimited multiplex stream.
  # Complete frames plus exact process completion establish quiescence.
  while True:
   header=r.read(8)
   if not header: break
   assert len(header)==8 and header[0] in (1,2) and header[1:4]==b'\x00\x00\x00'
   remaining=struct.unpack('>I',header[4:])[0];assert remaining<=16*1024*1024
   output(header)
   while remaining:
    chunk=r.read(min(65536,remaining));assert chunk
    remaining-=len(chunk);output(chunk)
  c.close();c=UnixHTTP('docker')
  for attempt in range(120):
   c.request('GET',inspect);finished=c.getresponse();receipt=json.loads(finished.read());assert finished.status==200
   assert receipt['ContainerID']==container and isinstance(receipt['ExitCode'],int)
   if not receipt['Running']: break
   time.sleep(0.25)
  assert not receipt['Running']
 elif r.chunked:
  # Consume strict chunk framing, including the terminating trailer line.
  # HTTPResponse's trailer reader otherwise accepts a premature EOF.
  while True:
   line=r.fp.readline(65537)
   assert len(line)<=65536 and line.endswith(b'\r\n')
   token=line[:-2].split(b';',1)[0]
   assert token and all(ch in b'0123456789abcdefABCDEF' for ch in token)
   remaining=int(token,16)
   if remaining==0:
    while True:
     trailer=r.fp.readline(65537)
     assert len(trailer)<=65536 and trailer.endswith(b'\r\n')
     if trailer==b'\r\n': break
     assert b':' in trailer
    break
   while remaining:
    chunk=r.fp.read(min(65536,remaining));assert chunk
    remaining-=len(chunk);output(chunk)
   assert r.fp.read(2)==b'\r\n'
 else:
  while r.length:
   chunk=r.read(min(65536,r.length));assert chunk
   output(chunk)
  assert r.length==0
 state['dirty']=False;save()
 # Only a verified daemon response can complete the caller's response framing.
 emit(b'0\r\n\r\n')
 c.close()
try: main()
except Exception: fail()
`
