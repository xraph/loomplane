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

const effectFenceScript = `import os,sys,json,hashlib,fcntl,socket,http.client,signal,tempfile,stat
# Once the complete request has arrived, caller loss must not interrupt the
# daemon request. An uncatchable termination leaves the durable dirty marker.
for sig in (signal.SIGHUP,signal.SIGTERM,signal.SIGINT): signal.signal(sig,signal.SIG_IGN)
out=sys.stdout.buffer
alive=True
def emit(data):
 global alive
 if alive:
  try: out.write(data);out.flush()
  except (BrokenPipeError,OSError): alive=False
def fail():
 emit(b'HTTP/1.1 409 Conflict\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
def main():
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
 headers={k:','.join(v) for k,v in (q['Headers'] or {}).items()};headers['Content-Length']=str(size)
 state['dirty']=True;save()
 c.request(q['Method'],q['Path'],body=payload,headers=headers);r=c.getresponse()
 emit(('HTTP/1.1 '+str(r.status)+' '+r.reason+'\r\n').encode())
 for k,v in r.getheaders():
  if k.lower() not in ('transfer-encoding','connection','content-length'): emit((k+': '+v+'\r\n').encode())
 emit(b'Connection: close\r\n\r\n')
 while True:
  chunk=r.read(65536)
  if not chunk: break
  emit(chunk)
 state['dirty']=False;save()
 c.close()
try: main()
except Exception: fail()
`
