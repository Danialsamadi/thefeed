package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// The install script shows each messenger limit's default when asking for it.
// A shell script cannot read them from here, so it repeats them; this keeps
// the two from drifting, which would offer operators a number the server does
// not actually use.
func TestInstallScriptChatDefaultsMatchServer(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	server := map[string]string{}
	for _, m := range regexp.MustCompile(`chatIntEnv\(\w+, (\d+), "(THEFEED_CHAT_\w+)"\)`).FindAllStringSubmatch(string(src), -1) {
		server[m[2]] = m[1]
	}
	asked := regexp.MustCompile(`"(THEFEED_CHAT_\w+)\|[^|]*\|(\d+)"`).FindAllStringSubmatch(string(script), -1)
	if len(asked) == 0 {
		t.Fatal("no messenger limits found in install.sh")
	}
	for _, m := range asked {
		key, def := m[1], m[2]
		want, ok := server[key]
		if !ok {
			t.Errorf("install.sh asks for %s, which the server does not read", key)
			continue
		}
		if def != want {
			t.Errorf("install.sh offers %s=%s by default, the server defaults to %s", key, def, want)
		}
	}
}

// installSh sources scripts/install.sh in bash and runs script after it, with
// every path the installer writes inside a temp dir ($T), and systemctl and
// ufw (reporting active) replaced by functions that record their arguments in
// $T/systemctl.log and $T/ufw.log. stdin answers the prompts. The installer
// sees none of the caller's THEFEED_*, TELEGRAM_*, SOURCE or GITHUB_REPO,
// only env (KEY=value). It returns $T.
func installSh(t *testing.T, stdin, script string, env ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	src, err := filepath.Abs("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pre := `source "$1" || exit 1
T="$2"
INSTALL_DIR="$T/opt"; DATA_DIR="$T/opt/data"
SERVICE_FILE="$T/thefeed-server.service"; ADMIN_SERVICE_FILE="$T/thefeed-admin.service"
mkdir -p "$DATA_DIR"
systemctl() { echo "$*" >> "$T/systemctl.log"; }
ufw() { echo "$*" >> "$T/ufw.log"; [[ "$1" == status ]] && echo "Status: active"; return 0; }
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "-c", pre+script, "bash", src, dir)
	cmd.Stdin = strings.NewReader(stdin)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "THEFEED_") && !strings.HasPrefix(k, "TELEGRAM_") && k != "SOURCE" && k != "GITHUB_REPO" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return dir
}

// The tests source install.sh, which then stops before main. Run as a file
// or piped (curl | bash), main still runs: --help gets past the guard to the
// root check, or to the help as root.
func TestInstallScriptRunsMainUnlessSourced(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	script := "../../scripts/install.sh"
	piped := exec.Command(bash, "-s", "--", "--help")
	piped.Stdin = strings.NewReader(readFile(t, script))
	for name, cmd := range map[string]*exec.Cmd{"file": exec.Command(bash, script, "--help"), "piped": piped} {
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "root privilege") && !strings.Contains(string(out), "Usage: bash") {
			t.Errorf("%s: main didn't run:\n%s", name, out)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// execStart joins a unit's ExecStart line and its continuations into words.
func execStart(t *testing.T, unit string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(unit, "\nExecStart=")
	if !ok {
		t.Fatalf("no ExecStart in:\n%s", unit)
	}
	var line strings.Builder
	for _, l := range strings.Split(rest, "\n") {
		more := strings.HasSuffix(l, `\`)
		line.WriteString(strings.TrimSuffix(l, `\`) + " ")
		if !more {
			break
		}
	}
	return strings.Fields(line.String())
}

// envFile reads thefeed.env as KEY -> values, every occurrence kept.
func envFile(t *testing.T, path string) map[string][]string {
	t.Helper()
	env := map[string][]string{}
	for _, l := range strings.Split(readFile(t, path), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
			env[k] = append(env[k], v)
		}
	}
	return env
}

// thefeed.env is the single source of every setting, so a fresh install
// writes all of them, each once and each a value the server accepts (it
// refuses to start on one it doesn't).
func TestInstallFreshEnvHasEverySetting(t *testing.T) {
	// No channels, no X accounts, no private invites, the domain, no extra,
	// messenger or proxy domains, the passphrase, then Enter for the rest.
	answers := "\n\n\nt.example.com\n\n\n\nsecret\n" + strings.Repeat("\n", 20)
	dir := installSh(t, answers, `setup_config`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	for _, s := range serverconfig.Settings {
		vals := env[s.Env]
		if len(vals) != 1 {
			t.Errorf("%s written %d times, want once", s.Env, len(vals))
			continue
		}
		if err := serverconfig.Validate(s.Key, vals[0]); err != nil {
			t.Errorf("%s=%s: %v", s.Env, vals[0], err)
		}
	}
	if got, want := env["THEFEED_DATA_DIR"], filepath.Join(dir, "opt", "data"); len(got) != 1 || got[0] != want {
		t.Errorf("THEFEED_DATA_DIR = %q, want %q", got, want)
	}
	if got := env["THEFEED_DOMAIN"]; len(got) != 1 || got[0] != "t.example.com" {
		t.Errorf("THEFEED_DOMAIN = %q, want the answer t.example.com", got)
	}
}

// The installer's settings table is how it fills thefeed.env and maps an old
// unit's flags back to env names, so it has to be the registry, row for row.
func TestInstallScriptSettingsMatchRegistry(t *testing.T) {
	dir := installSh(t, "", `printf '%s\n' "${SERVER_SETTINGS[@]}" > "$T/table"`)
	rows := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "table"))), "\n") {
		env, rest, _ := strings.Cut(l, "|")
		rows[env] = rest
	}
	for _, s := range serverconfig.Settings {
		if want := s.Flag + "|" + s.Default; rows[s.Env] != want {
			t.Errorf("install.sh has %s as %q, the registry as %q", s.Env, rows[s.Env], want)
		}
		delete(rows, s.Env)
	}
	for env := range rows {
		t.Errorf("install.sh lists %s, which the registry doesn't", env)
	}
}

// one is the single value of key in env, failing the test otherwise.
func one(t *testing.T, env map[string][]string, key string) string {
	t.Helper()
	if len(env[key]) != 1 {
		t.Fatalf("%s = %q, want one value", key, env[key])
	}
	return env[key][0]
}

// Re-installing over a unit that still passes settings as flags moves them
// into thefeed.env, keeping whichever value the server actually ran with:
// a typed flag beats the file, except where cmd/server lets the file win.
func TestInstallMovesOldUnitFlagsIntoEnv(t *testing.T) {
	oldUnit := `[Service]
EnvironmentFile=/opt/thefeed/data/thefeed.env
ExecStart=/opt/thefeed/thefeed-server \
    --data-dir /opt/thefeed/data \
    --domain ${THEFEED_DOMAIN} \
    --key ${THEFEED_KEY} \
    --channels /etc/thefeed/channels.txt \
    --session /var/lib/thefeed/session.json \
    --msg-limit 15 \
    --padding=0 \
    --listen ${THEFEED_LISTEN} --no-telegram --allow-manage --debug

Restart=on-failure
`
	dir := installSh(t, strings.Repeat("\n", 40), `cat > "$SERVICE_FILE" <<'E'
`+oldUnit+`E
cat > "$DATA_DIR/thefeed.env" <<'E'
THEFEED_DOMAIN=t.example.com
THEFEED_KEY=secret
THEFEED_LISTEN=0.0.0.0:53
THEFEED_MSG_LIMIT=30
THEFEED_ALLOW_MANAGE=0
TELEGRAM_API_ID=0
TELEGRAM_API_HASH=none
TELEGRAM_PHONE=none
THEFEED_NO_TELEGRAM=1
E
migrate_unit_flags && setup_config`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	for key, want := range map[string]string{
		"THEFEED_CHANNELS":     "/etc/thefeed/channels.txt",     // literal path: the old layout
		"THEFEED_SESSION":      "/var/lib/thefeed/session.json", // losing it logs Telegram out
		"THEFEED_PADDING":      "0",
		"THEFEED_MSG_LIMIT":    "30", // a flag at its default let the file through
		"THEFEED_ALLOW_MANAGE": "0",  // the file beats --allow-manage
		"THEFEED_DOMAIN":       "t.example.com",
		"THEFEED_DATA_DIR":     filepath.Join(dir, "opt", "data"), // the unit keeps --data-dir
	} {
		if got := one(t, env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"THEFEED_DEBUG", "THEFEED_NO_TELEGRAM"} {
		if v, ok := serverconfig.ParseBool(one(t, env, key)); !ok || !v {
			t.Errorf("%s = %q, want on", key, env[key])
		}
	}
	if got := readFile(t, filepath.Join(dir, "opt", "data", "thefeed-server.service.bak")); got != oldUnit {
		t.Errorf("backup of the old unit:\n%s\nwant:\n%s", got, oldUnit)
	}
}

// A hand-written unit with one bare flag and no --data-dir moves too.
func TestInstallMovesALoneFlag(t *testing.T) {
	dir := installSh(t, "", `printf 'ExecStart=/usr/local/bin/thefeed-server --no-telegram\n' > "$SERVICE_FILE"
printf 'THEFEED_DOMAIN=t.example.com\n' > "$DATA_DIR/thefeed.env"
migrate_unit_flags`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	if v, ok := serverconfig.ParseBool(one(t, env, "THEFEED_NO_TELEGRAM")); !ok || !v {
		t.Errorf("THEFEED_NO_TELEGRAM = %q, want on", env["THEFEED_NO_TELEGRAM"])
	}
}

// The panel writes switches as true/false. Re-running the installer and
// pressing Enter keeps every one of them as it was.
func TestInstallUpdateKeepsSwitchesThePanelWrote(t *testing.T) {
	dir := installSh(t, strings.Repeat("\n", 40), `cat > "$DATA_DIR/thefeed.env" <<'E'
THEFEED_DOMAIN=t.example.com
THEFEED_KEY=secret
THEFEED_ALLOW_MANAGE=true
THEFEED_DNS_MEDIA_ENABLED=true
THEFEED_GITHUB_RELAY_ENABLED=true
THEFEED_GITHUB_RELAY_TOKEN=tok
THEFEED_GITHUB_RELAY_REPO=owner/relay
TELEGRAM_API_ID=0
THEFEED_NO_TELEGRAM=true
E
setup_config`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	for _, key := range []string{"THEFEED_ALLOW_MANAGE", "THEFEED_DNS_MEDIA_ENABLED", "THEFEED_GITHUB_RELAY_ENABLED", "THEFEED_NO_TELEGRAM"} {
		if v, ok := serverconfig.ParseBool(one(t, env, key)); !ok || !v {
			t.Errorf("%s = %q after an update, want it still on", key, env[key])
		}
	}
	if got := one(t, env, "THEFEED_GITHUB_RELAY_TOKEN"); got != "tok" {
		t.Errorf("THEFEED_GITHUB_RELAY_TOKEN = %q, want tok kept", got)
	}
}

// The installer asks for the panel's hostname and records which release repo
// it installed from, the panel's default update source (a fork sets
// GITHUB_REPO). The panel unit reads both from thefeed.env.
func TestInstallSavesPanelHostnameAndReleaseRepo(t *testing.T) {
	// As the fresh install above, with a panel hostname after the relays.
	answers := "\n\n\nt.example.com\n\n\n\nsecret\n\n\n\n\n\npanel.example.com\n" + strings.Repeat("\n", 10)
	dir := installSh(t, answers, `setup_config`, "GITHUB_REPO=fork/thefeed")
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	if got := one(t, env, "THEFEED_ADMIN_HOSTNAME"); got != "panel.example.com" {
		t.Errorf("THEFEED_ADMIN_HOSTNAME = %q, want panel.example.com", got)
	}
	if got := one(t, env, "THEFEED_ADMIN_RELEASE_REPO"); got != "fork/thefeed" {
		t.Errorf("THEFEED_ADMIN_RELEASE_REPO = %q, want fork/thefeed", got)
	}
}

// The update command the installer prints has no GITHUB_REPO in it, so a
// later run without one stays on the recorded repo: it neither installs
// upstream's binaries over a fork's nor moves the panel's update source.
func TestInstallUpdateKeepsRecordedReleaseRepo(t *testing.T) {
	dir := installSh(t, strings.Repeat("\n", 40), `cat > "$DATA_DIR/thefeed.env" <<'E'
THEFEED_DOMAIN=t.example.com
THEFEED_KEY=secret
TELEGRAM_API_ID=0
THEFEED_NO_TELEGRAM=1
THEFEED_ADMIN_RELEASE_REPO=fork/thefeed
E
use_recorded_repo && setup_config
SOURCE=github
curl() { local a; for a; do :; done; echo "$a" > "$T/url"; : > "$2"; }
download_binary v9.9.9 thefeed-admin`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	if got := one(t, env, "THEFEED_ADMIN_RELEASE_REPO"); got != "fork/thefeed" {
		t.Errorf("THEFEED_ADMIN_RELEASE_REPO = %q after an update, want fork/thefeed", got)
	}
	if url := readFile(t, filepath.Join(dir, "url")); !strings.HasPrefix(url, "https://github.com/fork/thefeed/releases/") {
		t.Errorf("update downloads %s, not from the recorded fork/thefeed", url)
	}
}

// With the unit no longer passing --no-telegram, thefeed.env alone decides
// the mode: choosing Telegram on an update must turn the old switch off.
func TestInstallUpdateToTelegramModeTurnsNoTelegramOff(t *testing.T) {
	// Enter through the panel hostname, then mode 2 and its credentials.
	answers := strings.Repeat("\n", 14) + "2\n12345\nabc\n+15550100\n" + strings.Repeat("\n", 10)
	dir := installSh(t, answers, `cat > "$DATA_DIR/thefeed.env" <<'E'
THEFEED_DOMAIN=t.example.com
THEFEED_KEY=secret
TELEGRAM_API_ID=0
THEFEED_NO_TELEGRAM=1
E
setup_config`)
	env := envFile(t, filepath.Join(dir, "opt", "data", "thefeed.env"))
	if v, ok := serverconfig.ParseBool(one(t, env, "THEFEED_NO_TELEGRAM")); !ok || v {
		t.Errorf("THEFEED_NO_TELEGRAM = %q, want off", env["THEFEED_NO_TELEGRAM"])
	}
	if got := one(t, env, "TELEGRAM_API_ID"); got != "12345" {
		t.Errorf("TELEGRAM_API_ID = %q, want 12345", got)
	}
}

// The panel gets its own unit beside thefeed-server's, reading the same
// thefeed.env, and nothing ties it to thefeed-server: it has to keep running
// while that one is stopped, so it can start it again.
func TestInstallWritesPanelUnit(t *testing.T) {
	dir := installSh(t, "", `install_admin_service`)
	unit := readFile(t, filepath.Join(dir, "thefeed-admin.service"))
	got := execStart(t, unit)
	want := []string{filepath.Join(dir, "opt", "thefeed-admin"), "--data-dir", filepath.Join(dir, "opt", "data")}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ExecStart = %q, want %q", got, want)
	}
	if !strings.Contains(unit, "\nEnvironmentFile=-"+filepath.Join(dir, "opt", "data", "thefeed.env")+"\n") {
		t.Errorf("panel unit doesn't read thefeed.env:\n%s", unit)
	}
	for _, tie := range []string{"Requires=", "BindsTo=", "PartOf="} {
		if strings.Contains(unit, tie) {
			t.Errorf("panel unit has %s: it must outlive thefeed-server\n%s", tie, unit)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "systemctl.log")), "daemon-reload") {
		t.Error("systemd was not told to reload units")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "ufw.log")), "allow 443/tcp\n") {
		t.Error("an active ufw was not opened for 443/tcp")
	}
}

// --uninstall takes the panel away with the server. Piped (no terminal), it
// keeps the data, the panel's database included.
func TestInstallUninstallRemovesPanel(t *testing.T) {
	dir := installSh(t, "", `mkdir -p "$INSTALL_DIR/admin"
touch "$INSTALL_DIR/thefeed-server" "$INSTALL_DIR/thefeed-admin" "$SERVICE_FILE" "$ADMIN_SERVICE_FILE" \
    "$DATA_DIR/thefeed.env" "$INSTALL_DIR/admin/admin.db"
uninstall_thefeed`)
	for _, gone := range []string{"thefeed-admin.service", "opt/thefeed-admin", "thefeed-server.service", "opt/thefeed-server"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still there after --uninstall", gone)
		}
	}
	for _, kept := range []string{"opt/data/thefeed.env", "opt/admin/admin.db"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s: %v; data is kept unless asked", kept, err)
		}
	}
	log := readFile(t, filepath.Join(dir, "systemctl.log"))
	for _, cmd := range []string{"stop thefeed-admin", "disable thefeed-admin"} {
		if !strings.Contains(log, cmd+"\n") {
			t.Errorf("systemctl %s was not run; got:\n%s", cmd, log)
		}
	}
}

// workflowStep is the text of the build.yml step whose body contains marker.
func workflowStep(t *testing.T, yml, marker string) (step string, at int) {
	t.Helper()
	at = strings.Index(yml, marker)
	if at < 0 {
		t.Fatalf("build.yml has no step with %q", marker)
	}
	start := strings.LastIndex(yml[:at], "\n      - ")
	end := strings.Index(yml[at:], "\n      - ")
	if end < 0 {
		end = len(yml) - at
	}
	return yml[start : at+end], start
}

// Each release builds thefeed-admin under the name the installer downloads,
// and publishes SHA256SUMS over every asset, which the panel's update checks
// before it swaps a binary in.
func TestReleaseWorkflowBuildsPanelAndPublishesChecksums(t *testing.T) {
	yml := readFile(t, "../../.github/workflows/build.yml")

	build, _ := workflowStep(t, yml, "./cmd/thefeed-admin")
	for _, want := range []string{"if: matrix.goos == 'linux'", "-o build/thefeed-admin-${{ matrix.goos }}-${{ matrix.goarch }} ./cmd/thefeed-admin"} {
		if !strings.Contains(build, want) {
			t.Errorf("thefeed-admin build step lacks %q:\n%s", want, build)
		}
	}

	_, release, ok := strings.Cut(yml, "\n  release:\n")
	if !ok {
		t.Fatal("build.yml has no release job")
	}
	sums, sumsAt := workflowStep(t, release, "SHA256SUMS")
	publish, publishAt := workflowStep(t, release, "softprops/action-gh-release")
	if !strings.Contains(sums, "sha256sum") || !strings.Contains(sums, "working-directory: artifacts") {
		t.Errorf("checksum step should run sha256sum inside artifacts/, so names have no directory:\n%s", sums)
	}
	if sumsAt > publishAt || !strings.Contains(publish, "files: artifacts/*") {
		t.Errorf("SHA256SUMS must be written into artifacts/ before the release publishes artifacts/*:\n%s\n%s", sums, publish)
	}

	// The installer asks for that same asset name.
	dir := installSh(t, "", `SOURCE=github GITHUB_REPO=sartoopjj/thefeed
curl() { local a; for a; do :; done; echo "$a" > "$T/url"; : > "$2"; }
download_binary v9.9.9 thefeed-admin`)
	url := strings.TrimSpace(readFile(t, filepath.Join(dir, "url")))
	base := filepath.Base(url)
	if !strings.HasPrefix(url, "https://github.com/sartoopjj/thefeed/releases/download/v9.9.9/") ||
		(base != "thefeed-admin-linux-amd64" && base != "thefeed-admin-linux-arm64") {
		t.Errorf("installer downloads %s, not a thefeed-admin-linux-<arch> asset the workflow builds", url)
	}
	if _, err := os.Stat(filepath.Join(dir, "opt", "thefeed-admin")); err != nil {
		t.Errorf("downloaded panel binary: %v", err)
	}
}

// A flag on the command line beats thefeed.env, so any setting the unit
// passes as a flag silently undoes the panel's edit of it. The unit keeps
// only --data-dir.
func TestInstallServerUnitPassesOnlyDataDir(t *testing.T) {
	dir := installSh(t, "", `cat > "$DATA_DIR/thefeed.env" <<'E'
THEFEED_DOMAIN=t.example.com
THEFEED_KEY=secret
THEFEED_NO_TELEGRAM=1
THEFEED_ALLOW_MANAGE=1
E
install_service`)
	got := execStart(t, readFile(t, filepath.Join(dir, "thefeed-server.service")))
	want := []string{filepath.Join(dir, "opt", "thefeed-server"), "--data-dir", filepath.Join(dir, "opt", "data")}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ExecStart = %q, want %q", got, want)
	}
}
