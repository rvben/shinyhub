//go:build linux

package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run in the disposable VM as shiso-control after provisioning the fixture.
// Normal test runs do not need root, systemd, or extra OS accounts.
func TestSystemdRuntimeLive(t *testing.T) {
	socket := os.Getenv("NATIVE_BROKER_TEST_SOCKET")
	if socket == "" {
		t.Skip("disposable Linux/systemd fixture is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rt, err := NewSystemdRuntime(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	const base = "/var/lib/shinyhub-isolation-test"
	const python = "/opt/shinyhub-native-proto/venv/bin/python"
	t.Run("control config must be private", func(t *testing.T) {
		path := base + "/synthetic-public-config"
		if err := os.WriteFile(path, []byte("synthetic"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		if err := rt.ValidateControlPath(path); err == nil {
			t.Fatal("app-readable controller config was accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if err := rt.ValidateControlPath(path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("startup prepares even idle app storage", func(t *testing.T) {
		path := base + "/appdata/beta/synthetic-restore-probe"
		if err := os.WriteFile(path, []byte("synthetic"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if err := rt.PrepareStorage(ctx); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0660 || info.Sys().(*syscall.Stat_t).Gid != 22102 {
			t.Fatalf("idle app data was not prepared: %v %v", info, err)
		}
	})
	dir := base + "/apps/alpha/versions/v1"
	params := StartParams{AppID: 1, Slug: "alpha", Dir: dir, AppDataPath: base + "/appdata/alpha", AppCachePath: base + "/appcache/alpha", Port: 18481, Tier: "default", DeploymentID: 17, LaunchID: "synthetic-live-launch"}
	run := func(code string, env ...string) (string, ExitInfo) {
		t.Helper()
		p := params
		p.Command = []string{python, "-c", code}
		p.Env = env
		var out bytes.Buffer
		exit, err := rt.RunOnce(ctx, p, &out)
		if err != nil {
			t.Fatal(err)
		}
		return out.String(), exit
	}
	t.Run("separate identity and private storage", func(t *testing.T) {
		code := `import json, os, socket
def allowed(fn):
 try: fn(); return True
 except OSError: return False
s=socket.socket(socket.AF_UNIX,socket.SOCK_SEQPACKET)
print(json.dumps({"uid":os.getuid(),"groups":os.getgroups(),"control":allowed(lambda:open("/var/lib/shinyhub-isolation-test/control/auth-secret").read()),"broker":allowed(lambda:s.connect("/run/shinyhub-isolation-test/control.sock")),"other_data":allowed(lambda:os.listdir("/var/lib/shinyhub-isolation-test/appdata/beta")),"control_env":allowed(lambda:open("/proc/"+os.environ["CONTROL_PID"]+"/environ","rb").read()),"control_signal":allowed(lambda:os.kill(int(os.environ["CONTROL_PID"]),0)),"no_new_privs":"NoNewPrivs:\t1" in open("/proc/self/status").read(),"no_caps":"CapEff:\t0000000000000000" in open("/proc/self/status").read()}))
open("/var/lib/shinyhub-isolation-test/appdata/alpha/result.txt","w").write("durable data")`
		out, exit := run(code, fmt.Sprintf("CONTROL_PID=%d", os.Getpid()))
		if exit.Code != 0 {
			t.Fatalf("probe failed: %s %+v", out, exit)
		}
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &m) != nil {
			t.Fatal(out)
		}
		if m["uid"] != float64(22101) || m["control"] != false || m["broker"] != false || m["other_data"] != false || m["control_env"] != false || m["control_signal"] != false || m["no_new_privs"] != true || m["no_caps"] != true {
			t.Fatalf("isolation failed: %s", out)
		}
		data, err := os.ReadFile(base + "/appdata/alpha/result.txt")
		if err != nil || string(data) != "durable data" {
			t.Fatalf("controller cannot back up app data: %s %v", data, err)
		}
	})
	t.Run("publication lock follows physical worker lifetime", func(t *testing.T) {
		lock, err := os.OpenFile(base+"/control/publication.lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		p := params
		p.Command = []string{python, "-c", "import time;time.sleep(60)"}
		p.LifetimeFiles = []*os.File{lock}
		ep, err := rt.Start(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.RemoveContainer(ep.WorkerID)
		// Closing the controller copy must leave the same open-file lock alive in
		// the isolated worker, including after creating a fresh runtime client.
		_ = lock.Close()
		contender, err := os.OpenFile(base+"/control/publication.lock", os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer contender.Close()
		if err = syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			t.Fatal("publication lock was lost during systemd handoff")
		}
		recovered, err := NewSystemdRuntime(ctx, socket)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		items, err := recovered.ListByLabel(ManagedContainerFilterJSON)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range items {
			if item.ID == ep.WorkerID && item.State == "running" && item.Labels[LabelLaunchID] == p.LaunchID {
				found = true
			}
		}
		if !found {
			t.Fatal("worker cannot be recovered by its durable unit identity")
		}
		if err = recovered.RemoveContainer(ep.WorkerID); err != nil {
			t.Fatal(err)
		}
		if err = syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal("publication lock survived physical worker stop")
		}
	})
	t.Run("guard gates app code and closes without acknowledgement", func(t *testing.T) {
		marker := base + "/appdata/alpha/guarded.txt"
		_ = os.Remove(marker)
		p := params
		p.GuardUntilAcknowledged = true
		p.Command = []string{python, "-c", fmt.Sprintf("open(%q,'w').write('ran')", marker)}
		ep, err := rt.Start(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.RemoveContainer(ep.WorkerID)
		time.Sleep(100 * time.Millisecond)
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("app executed before durable acknowledgement")
		}
		_ = ep.StartupGuard.Close()
		_ = rt.Wait(ctx, ep.Handle)
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("closing guard launched app code")
		}
	})
	t.Run("actual Shiny survives client replacement", func(t *testing.T) {
		app := filepath.Join(dir, "live_app.py")
		source := `from shiny import App, render, ui
app_ui=ui.page_fluid(ui.input_numeric("n","Synthetic",2),ui.output_text("answer"))
def server(input,output,session):
 @render.text
 def answer():return str(input.n())
app=App(app_ui,server)
app.run(host="127.0.0.1",port=18481)`
		if err = os.WriteFile(app, []byte(source), 0640); err != nil {
			t.Fatal(err)
		}
		p := params
		p.Command = []string{python, app}
		ep, err := rt.Start(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.RemoveContainer(ep.WorkerID)
		deadline := time.Now().Add(10 * time.Second)
		for {
			res, err := http.Get(ep.URL)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("Shiny did not become ready")
			}
			time.Sleep(50 * time.Millisecond)
		}
		ws := `import json
from websockets.sync.client import connect
with connect("ws://127.0.0.1:18481/websocket/") as c:
 c.send(json.dumps({"method":"init","data":{"n":2,".clientdata_output_answer_hidden":False}}))
 def answer(expected):
  for i in range(20):
   if json.loads(c.recv(timeout=3)).get("values",{}).get("answer")==expected:return True
  return False
 assert answer("2")
 c.send(json.dumps({"method":"update","data":{"n":7}}))
 assert answer("7")
`
		if out, err := exec.CommandContext(ctx, python, "-c", ws).CombinedOutput(); err != nil {
			t.Fatalf("Shiny reactive websocket failed: %s %v", out, err)
		}
		newRT, err := NewSystemdRuntime(ctx, socket)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := newRT.InspectPID(ep.WorkerID)
		if err != nil || pid != ep.Handle.PID {
			t.Fatalf("durable PID mismatch: %d %v", pid, err)
		}
		if err = newRT.Signal(RunHandle{ContainerID: ep.WorkerID}, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err = newRT.Wait(ctx, RunHandle{ContainerID: ep.WorkerID}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("build uses app identity", func(t *testing.T) {
		var out bytes.Buffer
		err := rt.RunBuild(ctx, dir, []string{python, "-c", "import os;print(os.getuid())"}, nil, &out)
		if err != nil || strings.TrimSpace(out.String()) != "22101" {
			t.Fatalf("build escaped app identity: %s %v", out.String(), err)
		}
	})
	t.Run("job completion includes background descendants", func(t *testing.T) {
		marker := base + "/appdata/alpha/background-job-result"
		_ = os.Remove(marker)
		defer os.Remove(marker)
		code := `import subprocess
subprocess.Popen(["/usr/bin/python3", "-c", "import time;time.sleep(.3);open('/var/lib/shinyhub-isolation-test/appdata/alpha/background-job-result','w').write('complete')"], start_new_session=True)`
		_, exit := run(code)
		if exit.Code != 0 {
			t.Fatalf("job failed: %+v", exit)
		}
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != "complete" {
			t.Fatalf("job returned before its detached descendant completed: %q %v", data, err)
		}
	})
	t.Run("live limits and reclaim fallback preserve identity", func(t *testing.T) {
		p := params
		p.Command = []string{python, "-c", "import time; a=bytearray(16*1024*1024);time.sleep(60)"}
		p.MemoryLimitMB = 64
		p.CPUQuotaPercent = 100
		ep, err := rt.Start(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.RemoveContainer(ep.WorkerID)
		time.Sleep(100 * time.Millisecond)
		memory, cpu := 128, 150
		if err := rt.UpdateResources(ctx, ep.Handle, ResourceLimits{MemoryLimitMB: &memory, CPUQuotaPercent: &cpu}); err != nil {
			t.Fatal(err)
		}
		memory, cpu = 0, 0
		if err := rt.UpdateResources(ctx, ep.Handle, ResourceLimits{MemoryLimitMB: &memory, CPUQuotaPercent: &cpu}); err != nil {
			t.Fatal(err)
		}
		if _, rss, err := rt.Stats(ctx, ep.Handle); err != nil || rss == 0 {
			t.Fatalf("cross-UID process stats failed: %d %v", rss, err)
		}
		rt.SetSnapshot(true, 1)
		freed, err := rt.Suspend(ctx, ep.Handle)
		if err != nil {
			t.Fatal(err)
		}
		if freed {
			if _, err := rt.Resume(ctx, ep.Handle); err != nil {
				t.Fatal(err)
			}
		}
		state, err := rt.state(ctx, ep.Handle)
		if err != nil || state.Frozen || state.PID != ep.Handle.PID {
			t.Fatalf("reclaim did not leave a live, thawed identity: %+v %v", state, err)
		}
	})
	t.Run("memory exhaustion stays inside worker", func(t *testing.T) {
		p := params
		p.MemoryLimitMB = 64
		p.Command = []string{python, "-c", "a=[]\nwhile True:a.append(bytearray(8*1024*1024))"}
		exit, err := rt.RunOnce(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !exit.Signaled || exit.Code != -1 {
			t.Fatalf("memory limit was not enforced: %+v", exit)
		}
	})
	t.Run("stop includes detached descendants", func(t *testing.T) {
		marker := base + "/appdata/alpha/detached.json"
		_ = os.Remove(marker)
		p := params
		p.Command = []string{python, "-c", fmt.Sprintf("import os,time,json\nc=os.fork()\nif c:open(%q,'w').write(json.dumps([os.getpid(),c]))\nelse:os.setsid()\ntime.sleep(60)", marker)}
		ep, err := rt.Start(ctx, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.RemoveContainer(ep.WorkerID)
		var pids []int
		deadline := time.Now().Add(3 * time.Second)
		for {
			raw, err := os.ReadFile(marker)
			if err == nil && json.Unmarshal(raw, &pids) == nil && len(pids) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("descendant marker missing")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err = rt.RemoveContainer(ep.WorkerID); err != nil {
			t.Fatal(err)
		}
		for _, pid := range pids {
			if _, err = os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
				t.Fatalf("detached PID %d survived unit stop", pid)
			}
		}
	})
	t.Run("unknown app never falls back", func(t *testing.T) {
		p := params
		p.AppID = 999
		p.Command = []string{python, "-c", "raise RuntimeError('must never execute')"}
		if _, err := rt.Start(ctx, p, io.Discard); err == nil {
			t.Fatal("unknown app launched")
		}
	})
}
