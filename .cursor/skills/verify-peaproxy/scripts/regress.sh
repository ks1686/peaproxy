#!/usr/bin/env bash
# Real-process checks for verify-peaproxy. Run from any directory.
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
V=$SCRIPT_DIR/verify-peaproxy

GOENV_FILE=$(go env GOENV)
GOENV_BACKUP=$(mktemp)
GOENV_HAD=0
if [[ -f "$GOENV_FILE" ]]; then
  cp "$GOENV_FILE" "$GOENV_BACKUP"
  GOENV_HAD=1
fi

restore_goenv() {
  if [[ "$GOENV_HAD" == 1 ]]; then
    mkdir -p "$(dirname "$GOENV_FILE")"
    cp "$GOENV_BACKUP" "$GOENV_FILE"
  else
    rm -f "$GOENV_FILE"
  fi
  rm -f "$GOENV_BACKUP"
}

sweep() {
  local d ev
  for d in /tmp/pea-v-m2 /tmp/pea-v-noproc /tmp/pea-v-psfail /tmp/pea-v-m5 /tmp/pea-v-m5b /tmp/pea-v-m1 /tmp/pea-v-076f5; do
    if [[ -d "$d" && -f "$d/run.env" ]]; then
      ev=$(awk -F= '/^EVIDENCE=/{print substr($0, index($0, "=") + 1)}' "$d/run.env")
      if [[ -n "$ev" ]]; then
        PEAPROXY_VERIFY_STATE=$d PEAPROXY_VERIFY_EVIDENCE="$ev" "$V" cleanup >/tmp/pea-regress-sweep.txt 2>&1 || true
      fi
      local pid cmd
      for pid in $(awk -F= '/^(PID|MOCK_PID|BROWSER_PID)=/ {print substr($0, index($0, "=") + 1)}' "$d/run.env"); do
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
          cmd=$(ps -o command= -p "$pid" 2>/dev/null || true)
          case "$cmd" in
            *"$d"*) kill "$pid" 2>/dev/null || true ;;
          esac
        fi
      done
    fi
    if [[ -d "$d" ]]; then
      chmod -R u+w "$d" 2>/dev/null || true
      rm -rf "$d" || true
    fi
  done
  if [[ -n "${SLEEP_PID:-}" ]] && kill -0 "$SLEEP_PID" 2>/dev/null; then
    kill "$SLEEP_PID" 2>/dev/null || true
  fi
  restore_goenv
}
trap sweep EXIT

expect_die() {
  local needle=$1
  shift
  local out rc
  set +e
  out=$("$@" 2>&1)
  rc=$?
  set -e
  printf '%s\n' "$out"
  if [[ "$rc" -eq 0 ]]; then
    echo "FAIL: expected failure: $*"
    exit 1
  fi
  if ! printf '%s\n' "$out" | grep -F -q -- "$needle"; then
    echo "FAIL: missing [$needle]"
    exit 1
  fi
}

no_cleaned() {
  if printf '%s\n' "$1" | grep -qx 'cleaned'; then
    echo "FAIL: cleanup claimed cleaned"
    exit 1
  fi
}

tmp_names() {
  find /tmp -maxdepth 1 \( \
    -name 'com.google.Chrome*' -o \
    -name '.com.google.Chrome*' -o \
    -name 'playwright-artifacts-*' -o \
    -name '.org.chromium*' \
  \) -printf '%f\n' 2>/dev/null | sort
}

echo "=== L13 failure leaves no state ==="
rm -rf /tmp/pea-v-l13fail
rm -f /tmp/pea-ev-l13-blocker
echo not-a-directory > /tmp/pea-ev-l13-blocker
expect_die "could not create the evidence directory" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-l13fail \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-l13-blocker/child \
  "$V" launch
if [[ -e /tmp/pea-v-l13fail ]]; then
  echo "FAIL: failed launch left a state directory"
  exit 1
fi
if [[ ! -f /tmp/pea-ev-l13-blocker ]]; then
  echo "FAIL: evidence parent file was replaced"
  exit 1
fi
rm -f /tmp/pea-ev-l13-blocker
echo "L13 failure: nested evidence parent was a file. State directory was not left behind."

echo "=== M4 world-writable state ==="
rm -rf /tmp/pea-v-world /tmp/pea-ev-world /tmp/pr182-m4-world
mkdir -m 777 /tmp/pea-v-world
printf '%s\n' 'touch /tmp/pr182-m4-world' > /tmp/pea-v-world/run.env
expect_die "state directory must be mode 700" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-world PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-world "$V" launch
if [[ -e /tmp/pr182-m4-world || ! -d /tmp/pea-v-world ]]; then
  echo "FAIL: world-writable state was executed or deleted"
  exit 1
fi
rm -rf /tmp/pea-v-world
echo "M4 world-writable: refused before reading run.env."

echo "=== M4 foreign owner ==="
rm -rf /tmp/pea-v-foreign /tmp/pr182-m4-foreign
mkdir -m 777 /tmp/pea-v-foreign
printf '%s\n' 'touch /tmp/pr182-m4-foreign' > /tmp/pea-v-foreign/run.env
sudo chown nobody:nogroup /tmp/pea-v-foreign /tmp/pea-v-foreign/run.env
sudo chmod 700 /tmp/pea-v-foreign
expect_die "state directory must be owned by the current user" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-foreign PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-foreign "$V" doctor
if [[ -e /tmp/pr182-m4-foreign ]]; then
  echo "FAIL: foreign run.env was executed"
  exit 1
fi
sudo rm -rf /tmp/pea-v-foreign
echo "M4 foreign-owned: refused."

echo "=== M4 parse, do not execute ==="
rm -rf /tmp/pea-v-m4ok /tmp/pea-ev-m4ok /tmp/pr182-m4-exec /tmp/pr182-m4-subst
mkdir -m 700 /tmp/pea-v-m4ok /tmp/pea-ev-m4ok
printf '%s\n' 'touch /tmp/pr182-m4-exec' > /tmp/pea-v-m4ok/run.env
expect_die "run.env has a line that is not KEY=value" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-m4ok PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m4ok "$V" doctor
printf '%s\n' 'PID=$(touch /tmp/pr182-m4-subst)' > /tmp/pea-v-m4ok/run.env
expect_die "already has run.env" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-m4ok PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m4ok "$V" launch
if [[ -e /tmp/pr182-m4-exec || -e /tmp/pr182-m4-subst ]]; then
  echo "FAIL: run.env was executed"
  exit 1
fi
rm -rf /tmp/pea-v-m4ok /tmp/pea-ev-m4ok
echo "M4 parse: a shell line and a command substitution were not executed."

echo "=== read-only tree backstop ==="
rm -rf /tmp/pea-v-ro /tmp/pea-ev-ro
mkdir -m 700 /tmp/pea-v-ro /tmp/pea-ev-ro
mkdir -p /tmp/pea-v-ro/locked/dir
echo x > /tmp/pea-v-ro/locked/dir/file
chmod -R a-w /tmp/pea-v-ro/locked
PEAPROXY_VERIFY_STATE=/tmp/pea-v-ro PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-ro "$V" cleanup
if [[ -d /tmp/pea-v-ro ]]; then
  echo "FAIL: read-only tree survived cleanup"
  exit 1
fi
rm -rf /tmp/pea-ev-ro
echo "Backstop: cleanup removed a mode 700 state dir that held a read-only tree."

echo "=== M2 go env -w ==="
rm -rf /tmp/pea-v-m2 /tmp/pea-ev-m2
go env -w GOMODCACHE=/home/ubuntu/go/pkg/mod
env -u GOMODCACHE \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m2 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m2 \
  "$V" launch
if [[ -d /tmp/pea-v-m2/home/go/pkg/mod || -d /tmp/pea-v-m2/home/go/pkg/sumdb ]]; then
  echo "FAIL: Go wrote the module cache or checksum database under the state home"
  exit 1
fi
python3 - <<'PY'
import pathlib
text = pathlib.Path("/tmp/pea-v-m2/run.env").read_text()
vals = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
env = pathlib.Path(f"/proc/{vals['PID']}/environ").read_bytes().replace(b"\0", b"\n").decode()
want = {}
for line in env.splitlines():
    if line.startswith(("GOMODCACHE=", "GOENV=", "GOPATH=", "GOCACHE=", "HOME=", "TMPDIR=")):
        print(line)
        k, v = line.split("=", 1)
        want[k] = v
if want.get("GOMODCACHE") != "/home/ubuntu/go/pkg/mod":
    raise SystemExit("GOMODCACHE was not passed through")
if want.get("GOENV") != "/home/ubuntu/.config/go/env":
    raise SystemExit("GOENV was not passed through")
if want.get("GOPATH") != "/home/ubuntu/go":
    raise SystemExit("GOPATH was not passed through")
if want.get("TMPDIR") != "/tmp/pea-v-m2/home/tmp":
    raise SystemExit("TMPDIR is not under the state home")
PY
env -u GOMODCACHE \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m2 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m2 \
  "$V" cleanup
if [[ -d /tmp/pea-v-m2 ]]; then
  echo "FAIL: M2 state dir remains"
  exit 1
fi
rm -rf /tmp/pea-ev-m2
restore_goenv
GOENV_BACKUP=$(mktemp)
if [[ -f "$GOENV_FILE" ]]; then
  cp "$GOENV_FILE" "$GOENV_BACKUP"
  GOENV_HAD=1
else
  GOENV_HAD=0
fi
echo "M2: go env -w GOMODCACHE was passed through. Cleanup removed the state dir."

echo "=== M3 hidden /proc ==="
rm -rf /tmp/pea-v-noproc /tmp/pea-ev-noproc
set +e
timeout 90 unshare --user --map-root-user --mount bash -c '
  mount -t tmpfs none /proc || { echo MOUNT_FAIL; exit 2; }
  export PEAPROXY_VERIFY_STATE=/tmp/pea-v-noproc
  export PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-noproc
  '"$V"' launch
  echo launch_rc=$?
' > /tmp/pea-noproc.log 2>&1
noproc_rc=$?
set -e
echo "unshare rc=$noproc_rc"
tail -n 20 /tmp/pea-noproc.log
sleep 0.3
left=$(ps -eo pid,cmd | awk '/\/tmp\/pea-v-noproc/ && !/awk/ { print }' || true)
if [[ -n "$left" ]]; then
  echo "FAIL: serve leaked without /proc"
  printf '%s\n' "$left"
  echo "$left" | awk '{ print $1 }' | xargs -r kill || true
  exit 1
fi
echo "M3 hidden /proc: no serve left for /tmp/pea-v-noproc. See the log if go failed before start."
chmod -R u+w /tmp/pea-v-noproc 2>/dev/null || true
rm -rf /tmp/pea-v-noproc /tmp/pea-ev-noproc

echo "=== M3 ps failure still kills the child ==="
rm -rf /tmp/pea-v-psfail /tmp/pea-ev-psfail /tmp/pea-fakebin
mkdir -p /tmp/pea-fakebin /tmp/pea-ev-psfail
printf '%s\n' '#!/bin/sh' 'exit 1' > /tmp/pea-fakebin/ps
chmod +x /tmp/pea-fakebin/ps
set +e
env PATH="/tmp/pea-fakebin:$PATH" \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-psfail \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-psfail \
  "$V" launch > /tmp/pea-psfail.log 2>&1
ps_rc=$?
set -e
echo "psfail rc=$ps_rc"
tail -n 15 /tmp/pea-psfail.log
sleep 0.3
left=$(ps -eo pid,cmd | awk '/\/tmp\/pea-v-psfail/ && !/awk/ { print }' || true)
if [[ -n "$left" ]]; then
  echo "FAIL: serve leaked when ps failed"
  printf '%s\n' "$left"
  echo "$left" | awk '{ print $1 }' | xargs -r kill || true
  exit 1
fi
grep -q 'Stopped that process' /tmp/pea-psfail.log
rm -rf /tmp/pea-v-psfail /tmp/pea-ev-psfail /tmp/pea-fakebin
echo "M3 ps stub: launch stopped the new serve. Nothing for that state dir is running."

echo "=== M5 L11 L13 nested evidence ==="
rm -rf /tmp/pea-v-m5 /tmp/pea-ev-m5 /tmp/pr182-dangle-r4
mkdir -p /tmp/pea-ev-m5
before_tmp=$(tmp_names)
TZ=UTC \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" launch
mode=$(stat -c '%a' /tmp/pea-ev-m5/nested/run)
echo "nested evidence mode $mode"
if [[ "$mode" != "700" ]]; then
  echo "FAIL: nested evidence dir is not 700"
  exit 1
fi
TZ=UTC \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" mock start
TZ=UTC \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" browser start
PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" browser screenshot --path /tmp/pea-ev-m5/nested/run/shot.png
python3 - <<'PY'
import pathlib
text = pathlib.Path("/tmp/pea-v-m5/run.env").read_text()
vals = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
env = pathlib.Path(f"/proc/{vals['BROWSER_PID']}/environ").read_bytes().replace(b"\0", b"\n").decode()
tmpdir = ""
for line in env.splitlines():
    if line.startswith("TMPDIR="):
        tmpdir = line.split("=", 1)[1]
        print(line)
if tmpdir != "/tmp/pea-v-m5/home/tmp":
    raise SystemExit("browser TMPDIR is not the state temp dir")
PY
after_tmp=$(tmp_names)
if [[ "$before_tmp" != "$after_tmp" ]]; then
  echo "FAIL: browser run added temp dirs in /tmp"
  echo "before: $before_tmp"
  echo "after: $after_tmp"
  exit 1
fi
echo "L11: no new Chrome or Playwright temp dirs in /tmp. TMPDIR is the state temp dir."
python3 - <<'PY'
import os, pathlib, subprocess
text = pathlib.Path("/tmp/pea-v-m5/run.env").read_text()
vals = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
def lstart(tz, pid):
    out = subprocess.check_output(["ps", "-o", "lstart=", "-p", pid], env={**os.environ, "TZ": tz, "LC_ALL": "C"}, text=True)
    return out.strip()
rec = vals["PID_START"]
utc = lstart("UTC0", vals["PID"])
ny = lstart("America/New_York", vals["PID"])
print("recorded " + rec)
print("utc0 " + utc)
print("new_york " + ny)
if rec != utc:
    raise SystemExit("recorded start is not TZ=UTC0 lstart")
if rec == ny:
    raise SystemExit("New York lstart matched UTC, so this check would not catch the bug")
PY
TZ=America/New_York \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" doctor
TZ=Asia/Tokyo \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" doctor
echo "M5: doctor matched the UTC start time under America/New_York and Asia/Tokyo."
expect_die "from 1 to 120" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" api wait GET /healthz --contains ok --timeout 99999999999999999999
ln -s /tmp/pr182-dangle-r4 /tmp/pea-ev-m5/nested/run/dl.png
expect_die "refusing to write through a symlink" \
  env PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" browser screenshot --path /tmp/pea-ev-m5/nested/run/dl.png
if [[ -e /tmp/pr182-dangle-r4 ]]; then
  echo "FAIL: dangling symlink was followed"
  exit 1
fi
rm -f /tmp/pea-ev-m5/nested/run/dl.png
echo "dangling symlink refused"
cp -p /tmp/pea-v-m5/run.env /tmp/pea-v-m5/run.env.bak
python3 - <<'PY'
import pathlib
p = pathlib.Path("/tmp/pea-v-m5/run.env")
keys = {"PID_START", "MOCK_PID_START", "BROWSER_PID_START"}
out = []
for line in p.read_text().splitlines():
    key = line.split("=", 1)[0]
    if key in keys:
        out.append(key + "=forged-start")
    else:
        out.append(line)
p.write_text("\n".join(out) + "\n")
PY
serve_pid=$(awk -F= '/^PID=/{print $2}' /tmp/pea-v-m5/run.env)
mock_pid=$(awk -F= '/^MOCK_PID=/{print $2}' /tmp/pea-v-m5/run.env)
set +e
forged=$(TZ=America/New_York \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" cleanup 2>&1)
forged_rc=$?
set -e
printf '%s\n' "$forged"
if [[ "$forged_rc" -eq 0 ]]; then
  echo "FAIL: forged identity cleanup exited 0"
  exit 1
fi
no_cleaned "$forged"
if ! printf '%s\n' "$forged" | grep -F -q "State directory kept"; then
  echo "FAIL: forged cleanup did not say the state directory was kept"
  exit 1
fi
if [[ ! -d /tmp/pea-v-m5 ]]; then
  echo "FAIL: forged cleanup deleted the state directory"
  exit 1
fi
kill -0 "$serve_pid"
kill -0 "$mock_pid"
echo "M5 forged start: cleanup exited $forged_rc, kept the state dir, and left serve $serve_pid and mock $mock_pid running."
cp -p /tmp/pea-v-m5/run.env.bak /tmp/pea-v-m5/run.env
TZ=America/New_York \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5 \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5/nested/run \
  "$V" cleanup
if kill -0 "$serve_pid" 2>/dev/null || kill -0 "$mock_pid" 2>/dev/null; then
  echo "FAIL: New York cleanup left serve or mock running"
  exit 1
fi
if [[ -d /tmp/pea-v-m5 ]]; then
  echo "FAIL: New York cleanup left the state dir"
  exit 1
fi
after_clean=$(tmp_names)
if [[ "$before_tmp" != "$after_clean" ]]; then
  echo "FAIL: cleanup left new temp dirs in /tmp"
  echo "before: $before_tmp"
  echo "after: $after_clean"
  exit 1
fi
echo "M5: America/New_York cleanup stopped the real processes and removed the state dir."

echo "=== M5 second zone ==="
rm -rf /tmp/pea-v-m5b /tmp/pea-ev-m5b
TZ=UTC \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5b \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5b \
  "$V" launch
TZ=UTC \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5b \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5b \
  "$V" mock start
b_serve=$(awk -F= '/^PID=/{print $2}' /tmp/pea-v-m5b/run.env)
b_mock=$(awk -F= '/^MOCK_PID=/{print $2}' /tmp/pea-v-m5b/run.env)
TZ=Asia/Tokyo \
  PEAPROXY_VERIFY_STATE=/tmp/pea-v-m5b \
  PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m5b \
  "$V" cleanup
if kill -0 "$b_serve" 2>/dev/null || kill -0 "$b_mock" 2>/dev/null; then
  echo "FAIL: Tokyo cleanup left a process running"
  exit 1
fi
if [[ -d /tmp/pea-v-m5b ]]; then
  echo "FAIL: Tokyo cleanup left the state dir"
  exit 1
fi
rm -rf /tmp/pea-ev-m5b
echo "M5: Asia/Tokyo cleanup stopped the real processes and removed the state dir."

echo "=== M1 unrelated pid survives, cleanup stays honest ==="
rm -rf /tmp/pea-v-m1 /tmp/pea-ev-m1
PEAPROXY_VERIFY_STATE=/tmp/pea-v-m1 PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m1 "$V" launch
sleep 300 &
SLEEP_PID=$!
cp -p /tmp/pea-v-m1/run.env /tmp/pea-v-m1/run.env.bak
python3 - <<PY
import pathlib
p = pathlib.Path("/tmp/pea-v-m1/run.env")
out = []
for line in p.read_text().splitlines():
    key = line.split("=", 1)[0]
    if key == "MOCK_PID":
        out.append("MOCK_PID=$SLEEP_PID")
    elif key == "MOCK_PID_START":
        out.append("MOCK_PID_START=forged-start")
    else:
        out.append(line)
p.write_text("\\n".join(out) + "\\n")
PY
m1_serve=$(awk -F= '/^PID=/{print $2}' /tmp/pea-v-m1/run.env)
set +e
m1=$(PEAPROXY_VERIFY_STATE=/tmp/pea-v-m1 PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m1 "$V" cleanup 2>&1)
m1_rc=$?
set -e
printf '%s\n' "$m1"
if [[ "$m1_rc" -eq 0 ]]; then
  echo "FAIL: unrelated MOCK_PID cleanup exited 0"
  exit 1
fi
no_cleaned "$m1"
kill -0 "$SLEEP_PID"
if [[ ! -d /tmp/pea-v-m1 ]]; then
  echo "FAIL: unrelated-pid cleanup deleted the state dir"
  exit 1
fi
echo "M1: sleep $SLEEP_PID survived. Cleanup exited $m1_rc and kept the state dir."
cp -p /tmp/pea-v-m1/run.env.bak /tmp/pea-v-m1/run.env
PEAPROXY_VERIFY_STATE=/tmp/pea-v-m1 PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-m1 "$V" cleanup
if kill -0 "$m1_serve" 2>/dev/null; then
  echo "FAIL: restored cleanup left serve running"
  exit 1
fi
kill -0 "$SLEEP_PID"
kill "$SLEEP_PID" || true
wait "$SLEEP_PID" 2>/dev/null || true
SLEEP_PID=""
rm -rf /tmp/pea-ev-m1
echo "M1: after the recorded mock pid was restored, cleanup removed the state dir and the sleep was still alive until this script stopped it."

echo "=== Accounts journey ==="
export PEAPROXY_VERIFY_STATE=/tmp/pea-v-076f5
export PEAPROXY_VERIFY_EVIDENCE=/tmp/pea-ev-076f5
rm -rf "$PEAPROXY_VERIFY_STATE" "$PEAPROXY_VERIFY_EVIDENCE"
"$V" launch
pid=$(awk -F= '/^PID=/{print $2}' "$PEAPROXY_VERIFY_STATE/run.env")
kill -0 "$pid"
"$V" doctor
"$V" browser start
EVIDENCE=$PEAPROXY_VERIFY_EVIDENCE
"$V" browser wait --role heading --name "Add account"
"$V" browser wait --role button --name "Jan local"
"$V" browser screenshot --path "$EVIDENCE/accounts/before.png"
"$V" browser snapshot --path "$EVIDENCE/accounts/before.aria.txt"
"$V" browser click --role button --name "Jan local"
"$V" browser wait --text "Preset selected"
"$V" browser value --selector "#acc-id" | grep -qx 'jan-local'
"$V" browser click --role button --name "Add"
"$V" browser wait --text "Account added"
"$V" browser wait --selector '[data-del="jan-local"]'
"$V" browser screenshot --path "$EVIDENCE/accounts/added.png"
"$V" browser snapshot --path "$EVIDENCE/accounts/added.aria.txt"
"$V" browser reload
"$V" browser wait --selector '[data-del="jan-local"]'
"$V" api GET /admin/accounts > "$EVIDENCE/accounts/admin-after-ui.json"
grep -q '"id":"jan-local"' "$EVIDENCE/accounts/admin-after-ui.json"
"$V" cli -- accounts list > "$EVIDENCE/accounts/cli-list.txt"
grep -q 'jan-local' "$EVIDENCE/accounts/cli-list.txt"
"$V" browser click --selector '[data-del="jan-local"]'
"$V" browser wait --text "Account removed"
"$V" browser absent --selector '[data-del="jan-local"]'
"$V" cli -- accounts add jan-local > "$EVIDENCE/accounts/cli-add.txt"
grep -q 'added jan-local' "$EVIDENCE/accounts/cli-add.txt"
"$V" api wait GET /admin/accounts --contains '"id":"jan-local"' --timeout 15 >/tmp/pea-r4-adopt.txt
"$V" browser reload
"$V" browser wait --selector '[data-del="jan-local"]'
"$V" browser screenshot --path "$EVIDENCE/accounts/added-from-cli.png"
"$V" browser click --selector '[data-del="jan-local"]'
"$V" browser wait --text "Account removed"
"$V" browser absent --selector '[data-del="jan-local"]'
python3 - <<'PY'
import stat, pathlib
ev = pathlib.Path("/tmp/pea-ev-076f5")
mode = stat.S_IMODE(ev.stat().st_mode)
print(f"evidence dir {oct(mode)}")
if mode != 0o700:
    raise SystemExit("evidence dir is not 700")
for name in ["accounts/added.png", "accounts/added.aria.txt", "accounts/added-from-cli.png"]:
    m = stat.S_IMODE((ev / name).stat().st_mode)
    print(f"{name} {oct(m)}")
    if m != 0o600:
        raise SystemExit(name + " is not 600")
PY
"$V" doctor > "$EVIDENCE/doctor.txt"
cp "$PEAPROXY_VERIFY_STATE/serve.log" "$EVIDENCE/serve.log"
"$V" env > "$EVIDENCE/run.env"
chmod 600 "$EVIDENCE/serve.log" "$EVIDENCE/run.env" "$EVIDENCE/doctor.txt" "$EVIDENCE/accounts/"*.json "$EVIDENCE/accounts/"*.txt || true
printf 'accounts\nui add, persist, remove, cli add, ui adopt, remove\n' > "$EVIDENCE/feature.txt"
chmod 600 "$EVIDENCE/feature.txt"
"$V" cleanup
if kill -0 "$pid" 2>/dev/null; then
  echo "FAIL: serve $pid still alive"
  exit 1
fi
if [[ -d /tmp/pea-v-076f5 ]]; then
  echo "FAIL: journey state remains"
  exit 1
fi
"$V" evidence check accounts/added.png accounts/added.aria.txt accounts/added-from-cli.png serve.log run.env doctor.txt feature.txt
python3 - <<'PY'
import os, shutil, pathlib
src = pathlib.Path("/tmp/pea-ev-076f5")
dst = pathlib.Path("/opt/cursor/artifacts/peaproxy-verify-r4")
if dst.exists():
    shutil.rmtree(dst)
dst.mkdir(parents=True)
for dirpath, dirnames, filenames in os.walk(src):
    rel = pathlib.Path(dirpath).relative_to(src)
    target = dst / rel
    target.mkdir(exist_ok=True)
    for name in filenames:
        shutil.copyfile(pathlib.Path(dirpath) / name, target / name)
print("copied evidence to", dst)
PY
echo "Accounts journey green. serve pid $pid is gone. Evidence remains."
trap - EXIT
restore_goenv
echo "=== ALL GREEN ==="
