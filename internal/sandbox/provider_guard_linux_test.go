//go:build linux && integration

package sandbox

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These fixtures require a real namespace-enabled Linux kernel and ABI >=3.
// Missing capabilities may skip the ordinary integration suite; the explicit
// make accept-linux-provider-guard target treats every skip as a failure.
func guardNativeRoot(t *testing.T) string {
	t.Helper()
	if err := CheckProviderWriteProtection(); err != nil {
		t.Skip(err)
	}
	if err := namespaceCapabilityError(hasEffectiveCAPSYSADMIN()); err != nil {
		t.Skip(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("WT_PROVIDER_GUARD_FIXTURE_ROOT")
	if base == "" {
		base = filepath.Join(cwd, "..", "..", ".scratch")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "provider-guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

const guardPython = `import os,sys,errno
state,work,controller,ancestor,home,saved = sys.argv[1:]
def denied(fn):
 try: fn()
 except OSError as e:
  assert e.errno in (errno.EACCES,errno.EPERM,errno.EROFS,errno.EBUSY,errno.EXDEV),str(e)
 else: raise AssertionError("protected operation succeeded")
assert os.getcwd()==work
for private in (home+"/.ssh/synthetic-secret", "../.ssh/synthetic-secret", "/proc/self/cwd/../.ssh/synthetic-secret"):
 assert not os.path.exists(private),private
f = state+"/record"
if os.path.exists(saved):
 denied(lambda:open(saved+"/record","w").close())
 denied(lambda:os.rename(os.path.dirname(saved),os.path.dirname(saved)+"-replaced"))
assert open(f).read()=="original"
denied(lambda:open(f,"w").close())
denied(lambda:os.truncate(f,0))
denied(lambda:open(state+"/new","w").close())
denied(lambda:os.mkdir(state+"/new-dir"))
denied(lambda:os.unlink(f))
denied(lambda:os.rename(f,work+"/stolen"))
denied(lambda:os.link(f,work+"/hardlink"))
denied(lambda:os.rename(state,state+"-replaced"))
denied(lambda:os.rename(ancestor,ancestor+"-replaced"))
denied(lambda:open(controller,"w").close())
denied(lambda:os.unlink(controller))
denied(lambda:os.chmod(f,0o777))
os.symlink(f,work+"/alias")
denied(lambda:open(work+"/alias","w").close())
open(work+"/ordinary","w").write("workspace-ok")
open(work+"/atomic.tmp","w").write("atomic-ok")
os.rename(work+"/atomic.tmp",work+"/atomic")
open(home+"/.claude/config","w").write("provider-ok")
if os.path.exists(home+"/.claude.json"):
 open(home+"/.claude.json.tmp","w").write("adjacent-ok")
 os.rename(home+"/.claude.json.tmp",home+"/.claude.json")
print("protected-denials-and-normal-writes-ok")
`

func TestLinuxProviderGuardNative(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		name := "bind"
		if overlay {
			name = "overlay"
		}
		t.Run(name, func(t *testing.T) {
			root := guardNativeRoot(t)
			home, work, tmp := filepath.Join(root, "home"), filepath.Join(root, "home", "work"), filepath.Join(root, "tmp")
			state := filepath.Join(home, "authority", "state")
			controller := filepath.Join(root, "controller")
			for _, dir := range []string{state, work, tmp, filepath.Join(home, ".claude"), filepath.Join(home, ".ssh")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range []string{filepath.Join(state, "record"), controller, filepath.Join(home, ".claude.json"), filepath.Join(home, ".ssh", "synthetic-secret")} {
				if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("TMPDIR", tmp)
			cfg := Config{UserHome: home, Deny: []string{filepath.Join(home, ".ssh")}, Mounts: []Mount{{Source: work}, {Source: filepath.Join(home, ".claude"), UseRegex: overlay}}, ProtectedWriteTargets: []string{state, controller}}
			sb, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer sb.Destroy()
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			cmd, err := sb.Exec(context.Background(), python, []string{"-c", guardPython, state, work, controller, filepath.Dir(state), home, filepath.Join(sb.(*linuxSandbox).tmpDir, "real-home", "authority", "state")})
			if err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(os.Environ(), "HOME="+home, "TMPDIR="+tmp)
			cmd.Dir = work
			if !overlay { // bind mode cannot promise atomic updates of HOME siblings.
				if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil {
					t.Fatal(err)
				}
			}
			out, err := cmd.CombinedOutput()
			diag, _ := os.ReadFile(sb.DiagLog())
			if err != nil || !strings.Contains(string(out), "protected-denials-and-normal-writes-ok") {
				t.Fatalf("provider: %v\n%s\n%s", err, out, diag)
			}
			for file, want := range map[string]string{filepath.Join(state, "record"): "original", controller: "original", filepath.Join(work, "ordinary"): "workspace-ok", filepath.Join(home, ".claude", "config"): "provider-ok"} {
				got, err := os.ReadFile(file)
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q %v", file, got, err)
				}
			}
			if overlay {
				got, err := os.ReadFile(filepath.Join(home, ".claude.json"))
				if err != nil || string(got) != "adjacent-ok" {
					t.Fatalf("adjacent persistence: %q %v", got, err)
				}
			}
		})
	}
}

func TestLinuxProviderGuardLandlockNative(t *testing.T) {
	root := guardNativeRoot(t)
	work, blocked := filepath.Join(root, "work"), filepath.Join(root, "blocked")
	for _, dir := range []string{work, blocked} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, landlockExecArg, "--writable", work, "--", python, "-c", `import os,sys,errno,threading
work,blocked=sys.argv[1:]
open(work+"/allowed","w").write("ok")
def probe():
 try: open(blocked+"/denied","w").close()
 except OSError as e: assert e.errno==errno.EACCES
 else: os._exit(42)
t=threading.Thread(target=probe);t.start();t.join()
pid=os.fork()
if pid==0: probe();os._exit(0)
assert os.waitpid(pid,0)[1]==0
print("landlock-inheritance-ok")`, work, blocked)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "landlock-inheritance-ok") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestLinuxProviderGuardScopedNative(t *testing.T) {
	root := guardNativeRoot(t)
	home, work, tmp, state := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "tmp"), filepath.Join(root, "state")
	for _, dir := range []string{home, work, tmp, state, filepath.Join(home, ".claude"), filepath.Join(home, ".ssh")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(home, ".ssh", "synthetic-secret"), filepath.Join(state, "control.sock"), filepath.Join(state, "session", "claude-settings.json")} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	cfg := Config{UserHome: home, ReadOnlyFiles: []string{filepath.Join(state, "session", "claude-settings.json")}, Mounts: []Mount{{Source: work}, {Source: filepath.Join(home, ".claude"), UseRegex: true}}, Deny: []string{state, filepath.Join(state, "control.sock"), filepath.Join(home, ".ssh")}, DenyRename: []string{home}, ProtectedWriteTargets: []string{state}}
	sb, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := sb.Exec(context.Background(), python, []string{"-c", `import os,sys
work,state,visible,saved,upper,uid,hostpidns=sys.argv[1:]
assert not os.path.exists(visible+"/synthetic-secret")
assert not os.path.exists(saved+"/.ssh/synthetic-secret")
assert not os.path.exists("real-home/.ssh/synthetic-secret")
assert not os.path.exists("/proc/self/cwd/real-home/.ssh/synthetic-secret")
assert not os.path.exists("overlay-upper")
assert not os.path.exists("/proc/self/cwd/overlay-upper")
assert not os.path.exists(upper)
assert open(state+"/session/claude-settings.json").read()=="synthetic"
try: open(state+"/session/claude-settings.json","w").close()
except OSError: pass
else: raise AssertionError("runtime settings file writable")
assert not os.path.exists(state+"/control.sock")
try: open(state+"/marker","w").close()
except OSError: pass
else: raise AssertionError("protected state write succeeded")
open(work+"/normal","w").write("ok")
assert os.geteuid()==int(uid)
assert os.readlink("/proc/self/ns/pid")!=hostpidns
assert len(os.listdir("/proc"))>0
print("scoped-guard-ok")`, work, state, filepath.Join(home, ".ssh"), filepath.Join(sb.(*linuxSandbox).tmpDir, "real-home"), filepath.Join(sb.(*linuxSandbox).tmpDir, "overlay-upper"), fmt.Sprint(os.Getuid()), hostPIDNamespace(t)})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	diag, _ := os.ReadFile(sb.DiagLog())
	if err != nil || !strings.Contains(string(out), "scoped-guard-ok") {
		t.Fatalf("%v\n%s\n%s", err, out, diag)
	}
}

func hostPIDNamespace(t *testing.T) string {
	t.Helper()
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func TestLinuxProviderGuardRejectedTargetsNative(t *testing.T) {
	for _, kind := range []string{"missing", "fifo", "controller-hardlink", "state-hardlink", "external-symlink", "dangling-symlink", "socket-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := guardNativeRoot(t)
			work, tmp := filepath.Join(root, "work"), filepath.Join(root, "tmp")
			for _, dir := range []string{work, tmp} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", tmp)
			target := filepath.Join(root, "target")
			switch kind {
			case "fifo":
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "controller-hardlink":
				if err := os.WriteFile(target, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, filepath.Join(work, "alias")); err != nil {
					t.Fatal(err)
				}
			case "external-symlink", "dangling-symlink", "socket-hardlink":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "external-symlink":
					outside := filepath.Join(work, "clients.yaml")
					if err := os.WriteFile(outside, []byte("synthetic"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(target, "clients.yaml")); err != nil {
						t.Fatal(err)
					}
				case "dangling-symlink":
					if err := os.Symlink(filepath.Join(target, "absent"), filepath.Join(target, "clients.yaml")); err != nil {
						t.Fatal(err)
					}
				case "socket-hardlink":
					socket := filepath.Join(target, "control.sock")
					listener, err := net.Listen("unix", socket)
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					if err := os.Link(socket, filepath.Join(work, "socket-alias")); err != nil {
						t.Fatal(err)
					}
				}
			case "state-hardlink":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(target, "record")
				if err := os.WriteFile(file, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(file, filepath.Join(work, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			sb, err := New(Config{Mounts: []Mount{{Source: work}}, ProtectedWriteTargets: []string{target}})
			if err != nil {
				t.Fatal(err)
			}
			defer sb.Destroy()
			cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", `echo executed > "$1"`, "guard", filepath.Join(work, "executed")})
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			diag, _ := os.ReadFile(sb.DiagLog())
			if err == nil || !strings.Contains(string(diag), "refusing to launch agent") {
				t.Fatalf("unenforceable target admitted: %v %s %s", err, out, diag)
			}
			if _, err := os.Stat(filepath.Join(work, "executed")); !os.IsNotExist(err) {
				t.Fatal("provider ran before refusal")
			}
		})
	}
}

const guardAliasFixtureArg = "_test_linux_guard_mount_alias"

func init() {
	if len(os.Args) != 4 || os.Args[1] != guardAliasFixtureArg {
		return
	}
	root, kind := os.Args[2], os.Args[3]
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	target, alias := filepath.Join(root, "state"), filepath.Join(root, "work", "alias")
	if kind == "controller" {
		target, alias = filepath.Join(root, "controller"), filepath.Join(root, "tmp", "alias")
	}
	if err := unix.Mount(target, alias, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, err := New(Config{Mounts: []Mount{{Source: filepath.Join(root, "work")}}, ProtectedWriteTargets: []string{target}})
	if err == nil || !strings.Contains(err.Error(), "external mount alias") {
		fmt.Fprintf(os.Stderr, "alias admitted: %v", err)
		os.Exit(1)
	}
	fmt.Println("preexisting-bind-alias-refused")
	os.Exit(0)
}

func TestLinuxProviderGuardBindAliasesNative(t *testing.T) {
	for _, kind := range []string{"state", "controller"} {
		t.Run(kind, func(t *testing.T) {
			root := guardNativeRoot(t)
			for _, dir := range []string{"state", "work", "tmp", "work/alias"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range []string{"state/record", "controller", "tmp/alias"} {
				if err := os.WriteFile(filepath.Join(root, file), []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, guardAliasFixtureArg, root, kind)
			cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(root, "tmp"))
			cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "preexisting-bind-alias-refused") {
				t.Fatalf("%v %s", err, out)
			}
		})
	}
}
