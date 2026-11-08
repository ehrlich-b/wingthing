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
	root, err := os.MkdirTemp("/tmp", "wt")
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
open("/dev/null","w").write("device-io-ok")
import tempfile
with tempfile.NamedTemporaryFile(dir="/dev/shm") as shared:
 shared.write(b"shared-memory-ok");shared.flush()
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
			for _, dir := range []string{work, tmp, filepath.Join(root, "home")} {
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
			sb, err := New(Config{UserHome: filepath.Join(root, "home"), Mounts: []Mount{{Source: work}}, ProtectedWriteTargets: []string{target}})
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

const guardFilesystemFixtureArg = "_test_linux_guard_filesystem_contract"

// Each fixture mounts only disposable paths in its own user/mount namespace.
// ramfs is a real kernel filesystem deliberately absent from the allowlist;
// it exercises unknown-type refusal without pretending to mount a fake type.
func init() {
	if len(os.Args) != 5 || os.Args[1] != guardFilesystemFixtureArg {
		return
	}
	root, kind, stage := os.Args[2], os.Args[3], os.Args[4]
	if err := runGuardFilesystemFixture(root, kind, stage); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("preexisting-filesystem-refused-before-exec")
	os.Exit(0)
}

func runGuardFilesystemFixture(root, kind, stage string) error {
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return err
	}
	state, work, home := filepath.Join(root, "state"), filepath.Join(root, "work"), filepath.Join(root, "home")
	upper, lower, merged := state, filepath.Join(root, "lower"), filepath.Join(work, "view")
	fsType, rejected := "overlay", merged
	if strings.HasSuffix(kind, "-shm") {
		// Only this fixture's private mount namespace sees the replacement.
		if err := unix.Mount("tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1m"); err != nil {
			return err
		}
		merged, rejected = "/dev/shm/guard-view", "/dev/shm/guard-view"
		if err := os.Mkdir(merged, 0700); err != nil {
			return err
		}
	}
	switch kind {
	case "overlay-lower":
		upper, lower = filepath.Join(root, "upper"), state
	case "overlay-protected":
		upper, merged, rejected = filepath.Join(work, "upper"), state, state
	case "unknown-work":
		fsType, rejected = "ramfs", filepath.Join(work, "view")
	case "unknown-state":
		fsType, rejected = "ramfs", state
	case "unknown-home":
		fsType, rejected = "ramfs", home
	case "unknown-temp":
		fsType, rejected = "ramfs", filepath.Join(root, "tmp", "view")
	case "unknown-shm":
		fsType = "ramfs"
	case "device-regular":
		fsType, rejected = "device", "/dev/null"
	case "bind-work":
		fsType, rejected = "", filepath.Join(work, "view")
	}
	cfg := Config{UserHome: home, Mounts: []Mount{{Source: work}}, ProtectedWriteTargets: []string{state}}
	var sb Sandbox
	var err error
	if stage == "wrapper" {
		sb, err = New(cfg)
		if err != nil {
			return fmt.Errorf("plain fixture admission: %w", err)
		}
		defer sb.Destroy()
	}
	if fsType == "overlay" {
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s,userxattr,index=off", lower, upper, filepath.Join(root, "overlay-work"))
		if err := unix.Mount("overlay", merged, "overlay", 0, opts); err != nil {
			return fmt.Errorf("construct %s fixture: %w", kind, err)
		}
	} else if fsType == "ramfs" {
		if err := unix.Mount("ramfs", rejected, "ramfs", unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
			return fmt.Errorf("construct unknown filesystem fixture: %w", err)
		}
	} else {
		source := filepath.Join(root, "unrelated")
		if kind == "device-regular" {
			source = filepath.Join(root, "unrelated-device")
			if err := os.WriteFile(source, []byte("synthetic"), 0600); err != nil {
				return err
			}
		}
		if err := unix.Mount(source, rejected, "", unix.MS_BIND, ""); err != nil {
			return err
		}
		entries, err := readMountInfo("/proc/self/mountinfo")
		if err != nil {
			return err
		}
		fsType = entries[rejected].FSType
	}
	// Prove the mount is live before testing admission, with one finite write.
	if kind != "device-regular" {
		if err := os.WriteFile(filepath.Join(rejected, "fixture-ready"), []byte("synthetic"), 0600); err != nil {
			return fmt.Errorf("fixture mount not writable: %w", err)
		}
	}
	if stage == "admission" {
		sb, err = New(cfg)
		if sb != nil {
			sb.Destroy()
		}
		if err == nil || !strings.Contains(err.Error(), rejected) || !strings.Contains(err.Error(), fsType) {
			return fmt.Errorf("%s admission missing path/type refusal: %v", kind, err)
		}
		return nil
	}
	executed := filepath.Join(work, "executed")
	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", `echo executed > "$1"`, "guard", executed})
	if err != nil {
		return err
	}
	cmd.Env = os.Environ()
	out, execErr := cmd.CombinedOutput()
	diag, err := os.ReadFile(sb.DiagLog())
	if err != nil {
		return err
	}
	if execErr == nil || !strings.Contains(string(diag), "refusing to launch agent") || !strings.Contains(string(diag), rejected) || !strings.Contains(string(diag), fsType) {
		return fmt.Errorf("%s wrapper missing path/type refusal: %v %s %s", kind, execErr, out, diag)
	}
	if _, err := os.Stat(executed); !os.IsNotExist(err) {
		return fmt.Errorf("provider executed before filesystem refusal")
	}
	return nil
}

func TestLinuxProviderGuardFilesystemsNative(t *testing.T) {
	for _, kind := range []string{"overlay-upper", "overlay-lower", "overlay-protected", "overlay-shm", "unknown-work", "unknown-state", "unknown-home", "unknown-temp", "unknown-shm", "bind-work", "device-regular"} {
		t.Run(kind, func(t *testing.T) {
			for _, stage := range []string{"admission", "wrapper"} {
				t.Run(stage, func(t *testing.T) {
					root := guardNativeRoot(t)
					for _, dir := range []string{"state", "work/view", "work/upper", "tmp/view", "home", "lower", "upper", "overlay-work", "unrelated"} {
						if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
							t.Fatal(err)
						}
					}
					for _, file := range []string{"state/record", "lower/record", "work/upper/record"} {
						if err := os.WriteFile(filepath.Join(root, file), []byte("synthetic"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					exe, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command(exe, guardFilesystemFixtureArg, root, kind, stage)
					cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(root, "tmp"))
					cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}}
					out, err := cmd.CombinedOutput()
					if err != nil || !strings.Contains(string(out), "preexisting-filesystem-refused-before-exec") {
						t.Fatalf("%v %s", err, out)
					}
				})
			}
		})
	}
}
