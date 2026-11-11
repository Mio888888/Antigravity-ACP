package updater

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/version"
)

const (
	DefaultGitHubAPIBase   = "https://api.github.com"
	DefaultCLIManifestBase = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests"
	DefaultZedRegistryURL  = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

	SettingACPOverrideBinPath = "acp_override_binary_path"
	SettingACPInstalledMeta   = "acp_installed_meta_json"

	maxMetadataBytes = 8 << 20   // 8 MiB
	maxArchiveBytes  = 512 << 20 // 512 MiB
)

var semverTokenRe = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\b`)

type installedACPMeta struct {
	Flavor        string `json:"flavor"`
	Version       string `json:"version"`
	BinaryPath    string `json:"binary_path"`
	FileSHA256    string `json:"file_sha256"`
	PackageDigest string `json:"package_digest"`
	UpdatedAt     string `json:"updated_at"`
}

type RuntimeInfo struct {
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	DeployMode     string `json:"deploy_mode"`
	ExecutablePath string `json:"executable_path"`
}

type GatewayUpdateStatus struct {
	Repo             string `json:"repo"`
	CurrentVersion   string `json:"current_version"`
	Commit           string `json:"commit,omitempty"`
	LatestVersion    string `json:"latest_version"`
	ReleaseAvailable bool   `json:"release_available"`
	UpdateAvailable  bool   `json:"update_available"`
	CanSelfUpdate    bool   `json:"can_self_update"`
	BlockedReason    string `json:"blocked_reason,omitempty"`
	ManualCommand    string `json:"manual_command,omitempty"`
	AssetName        string `json:"asset_name,omitempty"`
	AssetSize        int64  `json:"asset_size,omitempty"`
	ReleaseURL       string `json:"release_url,omitempty"`
	PublishedAt      string `json:"published_at,omitempty"`
	ReleaseNotes     string `json:"release_notes,omitempty"`
	CheckError       string `json:"check_error,omitempty"`
}

type ACPUpdateStatus struct {
	BackendType      string `json:"backend_type"`
	Flavor           string `json:"flavor"`
	BinaryPath       string `json:"binary_path"`
	TargetInstallDir string `json:"target_install_dir"`
	CurrentVersion   string `json:"current_version"`
	LatestVersion    string `json:"latest_version"`
	UpdateAvailable  bool   `json:"update_available"`
	CanHotUpdate     bool   `json:"can_hot_update"`
	BlockedReason    string `json:"blocked_reason,omitempty"`
	CheckError       string `json:"check_error,omitempty"`
}

type StatusResponse struct {
	CheckedAt string              `json:"checked_at"`
	Updating  bool                `json:"updating"`
	Runtime   RuntimeInfo         `json:"runtime"`
	Gateway   GatewayUpdateStatus `json:"gateway"`
	ACP       ACPUpdateStatus     `json:"acp"`
}

type ApplyResult struct {
	UpdatedGateway bool   `json:"updated_gateway"`
	GatewayVersion string `json:"gateway_version,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	UpdatedACP     bool   `json:"updated_acp"`
	ACPVersion     string `json:"acp_version,omitempty"`
	ACPBinaryPath  string `json:"acp_binary_path,omitempty"`
	RetiredWorkers int    `json:"retired_workers,omitempty"`
	Restarting     bool   `json:"restarting"`
}

type ErrAlreadyUpdating struct{}

func (ErrAlreadyUpdating) Error() string {
	return "已有更新任务正在执行，请等待当前更新完成"
}

type Updater struct {
	mu                sync.Mutex
	updating          bool
	lastStatus        *StatusResponse
	lastCheckedAt     time.Time
	Repo              string
	GitHubAPIBase     string
	CLIManifestBase   string
	ZedRegistryURL    string
	AllowInsecureHTTP bool
	ExecPathOverride  string
	GOOSOverride      string
	GOARCHOverride    string
	DataDir           string
	Store             *storage.Store
	MetaClient        *http.Client
	DownloadClient    *http.Client
}

func NewUpdater(dataDir string, store *storage.Store) *Updater {
	u := &Updater{
		Repo:            version.EffectiveRepo(),
		GitHubAPIBase:   DefaultGitHubAPIBase,
		CLIManifestBase: DefaultCLIManifestBase,
		ZedRegistryURL:  DefaultZedRegistryURL,
		DataDir:         dataDir,
		Store:           store,
	}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("HTTP 重定向次数过多")
		}
		if req.URL.Scheme != "https" && !u.AllowInsecureHTTP {
			return fmt.Errorf("拒绝重定向至非 HTTPS 下载地址: %s", req.URL.String())
		}
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			req.Header.Del("Authorization")
		}
		return nil
	}
	u.MetaClient = &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: checkRedirect,
	}
	u.DownloadClient = &http.Client{
		Timeout:       10 * time.Minute,
		CheckRedirect: checkRedirect,
	}
	return u
}

// ResolveStartupACPOverride returns a validated hot-updated ACP binary path persisted in SQLite,
// ensuring flavor and OS/Arch compatibility and preferring configuredBinPath if it was upgraded externally.
func ResolveStartupACPOverride(store *storage.Store, backendType, configuredBinPath string) string {
	if store == nil {
		return ""
	}
	savedBin, ok := store.GetSetting(SettingACPOverrideBinPath)
	savedBin = strings.TrimSpace(savedBin)
	if !ok || savedBin == "" {
		return ""
	}
	st, err := os.Stat(savedBin)
	if err != nil || st.IsDir() {
		return ""
	}

	var meta *installedACPMeta
	if raw, mOK := store.GetSetting(SettingACPInstalledMeta); mOK && strings.TrimSpace(raw) != "" {
		var parsed installedACPMeta
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			meta = &parsed
		}
	}

	savedFlavor := determineACPFlavor("", savedBin)
	if meta != nil && strings.TrimSpace(meta.Flavor) != "" {
		savedFlavor = strings.TrimSpace(meta.Flavor)
	}
	normBackend := acp.NormalizeBackendType(backendType)
	if (normBackend == "cli" || normBackend == "zed") && savedFlavor != normBackend {
		return ""
	}
	if savedFlavor == "cli" {
		if VerifyExecutableBinary(savedBin, runtime.GOOS, runtime.GOARCH) != nil {
			return ""
		}
	}

	// If configuredBinPath resolves to a different binary on disk whose version is newer than savedBin
	// (e.g. after rebuilding a Docker image or running deploy/update.sh), prefer configuredBinPath.
	if strings.TrimSpace(configuredBinPath) != "" {
		resolvedCfg := acp.ResolveACPBinaryPath(backendType, configuredBinPath)
		if resolvedCfg != "" && filepath.Clean(resolvedCfg) != filepath.Clean(savedBin) {
			if cfgSt, cfgErr := os.Stat(resolvedCfg); cfgErr == nil && !cfgSt.IsDir() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				cfgVer, _ := detectLocalACPVersion(ctx, savedFlavor, resolvedCfg, nil)
				overrideVer, _ := detectLocalACPVersion(ctx, savedFlavor, savedBin, meta)
				cancel()
				if cfgVer != "" && overrideVer != "" && CompareVersions(overrideVer, cfgVer) < 0 {
					return ""
				}
			}
		}
	}
	return savedBin
}

func (u *Updater) effectiveGOOS() string {
	if u.GOOSOverride != "" {
		return u.GOOSOverride
	}
	return runtime.GOOS
}

func (u *Updater) effectiveGOARCH() string {
	if u.GOARCHOverride != "" {
		return u.GOARCHOverride
	}
	return runtime.GOARCH
}

func (u *Updater) resolveExecutablePath() (string, error) {
	if strings.TrimSpace(u.ExecPathOverride) != "" {
		return filepath.Abs(u.ExecPathOverride)
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if realP, err := filepath.EvalSymlinks(p); err == nil && realP != "" {
		return realP, nil
	}
	return filepath.Abs(p)
}

func isRunningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if raw, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(raw)
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") {
			return true
		}
	}
	return false
}

func probeDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".ag-perm-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

func (u *Updater) detectRuntime() (RuntimeInfo, bool, string, string) {
	goos := u.effectiveGOOS()
	goarch := u.effectiveGOARCH()
	execPath, execErr := u.resolveExecutablePath()

	info := RuntimeInfo{
		OS:             goos,
		Arch:           goarch,
		DeployMode:     "local",
		ExecutablePath: execPath,
	}

	if u.ExecPathOverride == "" && isRunningInContainer() {
		info.DeployMode = "docker"
		return info, false,
			"当前运行在 Docker 容器内，容器镜像文件系统不支持持久化原地更新主程序；请在宿主机执行下方命令重建容器镜像。",
			"./deploy/update.sh --mode docker"
	}

	if execErr != nil || execPath == "" {
		return info, false,
			fmt.Sprintf("无法定位当前网关可执行文件路径: %v", execErr),
			manualUpdateCommand(goos, "local")
	}

	if goos == "darwin" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			plist := filepath.Join(home, "Library", "LaunchAgents", "com.antigravity.gateway.plist")
			if _, err := os.Stat(plist); err == nil {
				info.DeployMode = "launchd"
			}
		}
	} else if goos == "linux" {
		if os.Getenv("INVOCATION_ID") != "" {
			info.DeployMode = "service"
		} else if _, err := os.Stat("/etc/systemd/system/antigravity-gateway.service"); err == nil && strings.HasPrefix(execPath, "/usr/") {
			info.DeployMode = "service"
		}
	}

	execDir := filepath.Dir(execPath)
	if wErr := probeDirWritable(execDir); wErr != nil {
		mode := info.DeployMode
		return info, false,
			fmt.Sprintf("当前进程对网关程序目录 (%s) 无写入权限 (%v)，通常因 systemd 安全沙箱或 root 属主保护限制；请在服务器终端执行下方更新命令。", execDir, wErr),
			manualUpdateCommand(goos, mode)
	}

	return info, true, "", manualUpdateCommand(goos, info.DeployMode)
}

func manualUpdateCommand(goos, mode string) string {
	if goos == "windows" {
		if mode == "service" {
			return `.\deploy\update.ps1 -Mode service`
		}
		return `.\deploy\update.ps1`
	}
	if mode == "service" {
		return "./deploy/update.sh --mode service"
	}
	if mode == "docker" {
		return "./deploy/update.sh --mode docker"
	}
	return "./deploy/update.sh"
}

func expectedGatewayAssetName(goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("antigravity-gateway-%s-%s%s", goos, goarch, ext)
}

func expectedGatewayBinaryName(goos string) string {
	if goos == "windows" {
		return "antigravity-gateway.exe"
	}
	return "antigravity-gateway"
}

func expectedProbeBinaryName(goos string) string {
	if goos == "windows" {
		return "antigravity-probe.exe"
	}
	return "antigravity-probe"
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func releaseAssetDownloadURL(a githubReleaseAsset) string {
	if strings.TrimSpace(os.Getenv("AG_GITHUB_TOKEN")) != "" && strings.TrimSpace(a.URL) != "" {
		return strings.TrimSpace(a.URL)
	}
	return strings.TrimSpace(a.BrowserDownloadURL)
}

type githubReleasePayload struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Body        string               `json:"body"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	Assets      []githubReleaseAsset `json:"assets"`
}

func (u *Updater) validateRemoteURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("无效的远程下载地址: %q", rawURL)
	}
	if parsed.Scheme != "https" && !u.AllowInsecureHTTP {
		return fmt.Errorf("拒绝非 HTTPS 下载地址: %s", rawURL)
	}
	return nil
}

func (u *Updater) isConfiguredGitHubHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "api.github.com") || strings.EqualFold(host, "github.com") {
		return true
	}
	if parsed, err := url.Parse(strings.TrimSpace(u.GitHubAPIBase)); err == nil && parsed.Host != "" {
		return strings.EqualFold(host, parsed.Host)
	}
	return false
}

func (u *Updater) addGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Antigravity-ACP-Gateway/"+strings.TrimPrefix(version.Version, "v"))
	if !u.isConfiguredGitHubHost(req.URL.Host) {
		return
	}
	tok := strings.TrimSpace(os.Getenv("AG_GITHUB_TOKEN"))
	if tok == "" {
		tok = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func (u *Updater) fetchLatestGitHubRelease(ctx context.Context) (*githubReleasePayload, bool, error) {
	repo := strings.TrimSpace(u.Repo)
	if repo == "" {
		repo = version.EffectiveRepo()
	}
	if repo == "" || !strings.Contains(repo, "/") {
		return nil, false, fmt.Errorf("未配置有效的 GitHub 仓库路径 (owner/repo)")
	}
	base := strings.TrimRight(u.GitHubAPIBase, "/")
	if base == "" {
		base = DefaultGitHubAPIBase
	}
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", base, repo)
	if err := u.validateRemoteURL(endpoint); err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	u.addGitHubHeaders(req)

	client := u.MetaClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("请求 GitHub Releases 接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return nil, false, fmt.Errorf("读取 GitHub Releases 响应失败: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		return nil, false, fmt.Errorf("GitHub Releases 接口返回 HTTP %d: %s", resp.StatusCode, msg)
	}
	var rel githubReleasePayload
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, false, fmt.Errorf("解析 GitHub Release JSON 失败: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, false, nil
	}
	return &rel, true, nil
}

func findReleaseAsset(assets []githubReleaseAsset, want string) (githubReleaseAsset, bool) {
	for _, a := range assets {
		if a.Name == want {
			return a, true
		}
	}
	for _, a := range assets {
		if strings.EqualFold(a.Name, want) {
			return a, true
		}
	}
	return githubReleaseAsset{}, false
}

func findChecksumAsset(assets []githubReleaseAsset) (githubReleaseAsset, bool) {
	for _, name := range []string{"SHA256SUMS", "SHA256SUMS.txt", "checksums.txt"} {
		if a, ok := findReleaseAsset(assets, name); ok {
			return a, true
		}
	}
	return githubReleaseAsset{}, false
}

func parseChecksumForFile(checksumText, assetName string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(checksumText))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(fields[0]))
		target := strings.TrimPrefix(strings.TrimSpace(fields[len(fields)-1]), "*")
		target = filepath.Base(strings.ReplaceAll(target, `\`, `/`))
		if target == assetName {
			if len(digest) != 64 || !isLowerHex(digest) {
				return "", fmt.Errorf("SHA256SUMS 中 %s 的哈希格式无效: %q", assetName, digest)
			}
			return digest, nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS 中未找到产物 %s 的 SHA-256 校验值", assetName)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

type cliManifestPayload struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA512  string `json:"sha512"`
}

type zedRegistryPayload struct {
	Agents []struct {
		ID           string `json:"id"`
		Version      string `json:"version"`
		Distribution struct {
			Binary map[string]struct {
				Archive string `json:"archive"`
				SHA256  string `json:"sha256"`
			} `json:"binary"`
		} `json:"distribution"`
	} `json:"agents"`
}

func isMuslLinux() bool {
	for _, p := range []string{"/lib/libc.musl-x86_64.so.1", "/lib/libc.musl-aarch64.so.1"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func cliPlatformKey(goos, goarch string) (string, error) {
	var archKey string
	switch goarch {
	case "amd64":
		archKey = "amd64"
	case "arm64":
		archKey = "arm64"
	default:
		return "", fmt.Errorf("当前架构 (%s) 不支持自动下载 Antigravity CLI", goarch)
	}
	switch goos {
	case "darwin":
		return "darwin_" + archKey, nil
	case "windows":
		return "windows_" + archKey, nil
	case "linux":
		if isMuslLinux() {
			return "linux_" + archKey + "_musl", nil
		}
		return "linux_" + archKey, nil
	default:
		return "", fmt.Errorf("当前操作系统 (%s) 不支持自动下载 Antigravity CLI", goos)
	}
}

func zedRegistryTarget(goos, goarch string) (string, error) {
	var regArch string
	switch goarch {
	case "amd64":
		regArch = "x86_64"
	case "arm64":
		regArch = "aarch64"
	default:
		return "", fmt.Errorf("当前架构 (%s) 不支持自动下载 Zed ACP 包", goarch)
	}
	switch goos {
	case "darwin", "linux", "windows":
		return goos + "-" + regArch, nil
	default:
		return "", fmt.Errorf("当前操作系统 (%s) 不支持自动下载 Zed ACP 包", goos)
	}
}

func determineACPFlavor(backendType, resolvedBin string) string {
	norm := acp.NormalizeBackendType(backendType)
	if norm == "zed" {
		return "zed"
	}
	if norm == "cli" {
		return "cli"
	}
	lowerBase := strings.ToLower(filepath.Base(resolvedBin))
	if strings.HasSuffix(lowerBase, ".par") || strings.Contains(lowerBase, "agy_acp_server") {
		return "zed"
	}
	return "cli"
}

func isExternalAppManagedACPDir(dir string) bool {
	slash := strings.ToLower(filepath.ToSlash(filepath.Clean(dir)))
	return strings.Contains(slash, "/external_agents/registry/")
}

func (u *Updater) resolveACPInstallDir(resolvedBin string) (string, bool, string) {
	if resolvedBin != "" && filepath.IsAbs(resolvedBin) {
		candDir := filepath.Dir(resolvedBin)
		if !isExternalAppManagedACPDir(candDir) {
			if st, err := os.Stat(candDir); err == nil && st.IsDir() && probeDirWritable(candDir) == nil {
				return candDir, true, ""
			}
		}
	}
	localBin, err := filepath.Abs("./bin")
	if err == nil {
		if st, sErr := os.Stat(localBin); sErr == nil && st.IsDir() && probeDirWritable(localBin) == nil {
			return localBin, true, ""
		}
	}
	if strings.TrimSpace(u.DataDir) != "" {
		dataBin, err := filepath.Abs(filepath.Join(u.DataDir, "bin"))
		if err == nil {
			if mkErr := os.MkdirAll(dataBin, 0700); mkErr == nil {
				if wErr := probeDirWritable(dataBin); wErr == nil {
					return dataBin, true, ""
				} else {
					return dataBin, false, fmt.Sprintf("数据目录 (%s) 无写入权限: %v", dataBin, wErr)
				}
			} else {
				return dataBin, false, fmt.Sprintf("无法创建 ACP 更新目录 (%s): %v", dataBin, mkErr)
			}
		}
	}
	return "", false, "未找到可写的 ACP 二进制安装目录"
}

func computeFileDigestHex(path string, h hash.Hash) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (u *Updater) loadStoredACPMeta() *installedACPMeta {
	if u.Store == nil {
		return nil
	}
	raw, ok := u.Store.GetSetting(SettingACPInstalledMeta)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var meta installedACPMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	return &meta
}

func (u *Updater) saveStoredACPMeta(meta installedACPMeta) {
	if u.Store == nil {
		return
	}
	if raw, err := json.Marshal(meta); err == nil {
		_ = u.Store.SetSetting(SettingACPInstalledMeta, string(raw))
	}
}

func detectLocalACPVersion(ctx context.Context, flavor, resolvedBin string, stored *installedACPMeta) (ver string, pkgDigest string) {
	if resolvedBin == "" {
		return "", ""
	}
	st, err := os.Stat(resolvedBin)
	if err != nil || st.IsDir() {
		return "", ""
	}
	if stored != nil && stored.BinaryPath != "" {
		samePath := filepath.Clean(stored.BinaryPath) == filepath.Clean(resolvedBin)
		if samePath && stored.FileSHA256 != "" {
			if actualSHA, hErr := computeFileDigestHex(resolvedBin, sha256.New()); hErr == nil && strings.EqualFold(actualSHA, stored.FileSHA256) {
				if stored.Version != "" {
					return NormalizeVersionDisplay(stored.Version), stored.PackageDigest
				}
				pkgDigest = stored.PackageDigest
			}
		}
	}
	if flavor == "zed" {
		markerPath := filepath.Join(filepath.Dir(resolvedBin), ".zed-acp-version")
		if rawMarker, mErr := os.ReadFile(markerPath); mErr == nil {
			fields := strings.Fields(string(rawMarker))
			if len(fields) >= 1 {
				if len(fields) >= 2 && pkgDigest == "" {
					candDigest := strings.ToLower(strings.TrimSpace(fields[1]))
					if len(candDigest) == 64 && isLowerHex(candDigest) {
						pkgDigest = candDigest
					}
				}
				if m := semverTokenRe.FindStringSubmatch(fields[0]); len(m) == 2 {
					return NormalizeVersionDisplay(m[1]), pkgDigest
				}
				return NormalizeVersionDisplay(fields[0]), pkgDigest
			}
		}
		parts := strings.Split(filepath.ToSlash(resolvedBin), "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "antigravity-acp" {
				if m := semverTokenRe.FindStringSubmatch(parts[i+1]); len(m) == 2 {
					return NormalizeVersionDisplay(m[1]), pkgDigest
				}
			}
		}
		return "", pkgDigest
	}
	// CLI: run `<resolvedBin> --version` with closed stdin and strict 3s timeout.
	vCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vCtx, resolvedBin, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", pkgDigest
	}
	firstLine := strings.TrimSpace(strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")[0])
	if m := semverTokenRe.FindStringSubmatch(firstLine); len(m) == 2 {
		return NormalizeVersionDisplay(m[1]), pkgDigest
	}
	return firstLine, pkgDigest
}

func (u *Updater) fetchCLIManifest(ctx context.Context, goos, goarch string) (*cliManifestPayload, error) {
	plat, err := cliPlatformKey(goos, goarch)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(u.CLIManifestBase, "/")
	if base == "" {
		base = DefaultCLIManifestBase
	}
	manifestURL := fmt.Sprintf("%s/%s.json", base, plat)
	if err := u.validateRemoteURL(manifestURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Antigravity-ACP-Gateway/"+strings.TrimPrefix(version.Version, "v"))
	client := u.MetaClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Antigravity CLI 清单失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 CLI 清单失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CLI 清单返回 HTTP %d", resp.StatusCode)
	}
	var m cliManifestPayload
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("解析 CLI 清单 JSON 失败: %w", err)
	}
	m.URL = strings.TrimSpace(m.URL)
	m.SHA512 = strings.ToLower(strings.TrimSpace(m.SHA512))
	if m.URL == "" {
		return nil, fmt.Errorf("CLI 清单缺少下载地址")
	}
	if err := u.validateRemoteURL(m.URL); err != nil {
		return nil, err
	}
	if len(m.SHA512) != 128 || !isLowerHex(m.SHA512) {
		return nil, fmt.Errorf("CLI 清单缺少有效的 128 位 SHA-512 校验值")
	}
	return &m, nil
}

type zedPackageSpec struct {
	Version    string
	ArchiveURL string
	SHA256     string
}

func (u *Updater) fetchZedPackageSpec(ctx context.Context, goos, goarch string) (*zedPackageSpec, error) {
	target, err := zedRegistryTarget(goos, goarch)
	if err != nil {
		return nil, err
	}
	regURL := strings.TrimSpace(u.ZedRegistryURL)
	if regURL == "" {
		regURL = DefaultZedRegistryURL
	}
	if err := u.validateRemoteURL(regURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, regURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Antigravity-ACP-Gateway/"+strings.TrimPrefix(version.Version, "v"))
	client := u.MetaClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求官方 ACP Registry 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 ACP Registry 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ACP Registry 返回 HTTP %d", resp.StatusCode)
	}
	var reg zedRegistryPayload
	if err := json.Unmarshal(body, &reg); err != nil {
		return nil, fmt.Errorf("解析 ACP Registry JSON 失败: %w", err)
	}
	for _, agent := range reg.Agents {
		if agent.ID != "antigravity-acp" {
			continue
		}
		binSpec, ok := agent.Distribution.Binary[target]
		if !ok || strings.TrimSpace(binSpec.Archive) == "" {
			return nil, fmt.Errorf("ACP Registry 中没有目标平台 %s 的 Zed ACP 产物", target)
		}
		sha := strings.ToLower(strings.TrimSpace(binSpec.SHA256))
		if len(sha) != 64 || !isLowerHex(sha) {
			return nil, fmt.Errorf("ACP Registry 中 %s 缺少有效的 64 位 SHA-256 校验值", target)
		}
		if err := u.validateRemoteURL(binSpec.Archive); err != nil {
			return nil, err
		}
		return &zedPackageSpec{
			Version:    strings.TrimSpace(agent.Version),
			ArchiveURL: strings.TrimSpace(binSpec.Archive),
			SHA256:     sha,
		}, nil
	}
	return nil, fmt.Errorf("ACP Registry 中未找到 antigravity-acp 条目")
}

// GetStatus returns cached or freshly checked update metadata for both Gateway and ACP backend.
func (u *Updater) GetStatus(ctx context.Context, backendType, configuredBinPath string, forceRefresh bool) StatusResponse {
	u.mu.Lock()
	updating := u.updating
	cached := u.lastStatus
	cachedAge := time.Since(u.lastCheckedAt)
	u.mu.Unlock()

	if !forceRefresh && cached != nil && cachedAge < 3*time.Minute {
		cp := *cached
		cp.Updating = updating
		return cp
	}

	rtInfo, canSelfUpdateGW, gwBlockedReason, manualCmd := u.detectRuntime()
	repo := strings.TrimSpace(u.Repo)
	if repo == "" {
		repo = version.EffectiveRepo()
	}

	gwStatus := GatewayUpdateStatus{
		Repo:           repo,
		CurrentVersion: NormalizeVersionDisplay(version.Version),
		Commit:         version.Commit,
		CanSelfUpdate:  canSelfUpdateGW,
		BlockedReason:  gwBlockedReason,
		ManualCommand:  manualCmd,
		AssetName:      expectedGatewayAssetName(rtInfo.OS, rtInfo.Arch),
	}

	resolvedACP := acp.ResolveACPBinaryPath(backendType, configuredBinPath)
	flavor := determineACPFlavor(backendType, resolvedACP)
	installDir, canHotUpdateACP, acpBlockedReason := u.resolveACPInstallDir(resolvedACP)
	storedMeta := u.loadStoredACPMeta()
	curACPVer, curACPPkgDigest := detectLocalACPVersion(ctx, flavor, resolvedACP, storedMeta)

	acpStatus := ACPUpdateStatus{
		BackendType:      backendType,
		Flavor:           flavor,
		BinaryPath:       resolvedACP,
		TargetInstallDir: installDir,
		CurrentVersion:   curACPVer,
		CanHotUpdate:     canHotUpdateACP,
		BlockedReason:    acpBlockedReason,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rel, found, err := u.fetchLatestGitHubRelease(ctx)
		if err != nil {
			gwStatus.CheckError = err.Error()
			return
		}
		if !found || rel == nil {
			gwStatus.ReleaseAvailable = false
			return
		}
		gwStatus.ReleaseAvailable = true
		gwStatus.LatestVersion = NormalizeVersionDisplay(rel.TagName)
		gwStatus.ReleaseURL = rel.HTMLURL
		gwStatus.PublishedAt = rel.PublishedAt
		gwStatus.ReleaseNotes = strings.TrimSpace(rel.Body)
		if asset, ok := findReleaseAsset(rel.Assets, gwStatus.AssetName); ok {
			gwStatus.AssetSize = asset.Size
		} else {
			gwStatus.CheckError = fmt.Sprintf("Release %s 中未包含当前平台产物 (%s)", rel.TagName, gwStatus.AssetName)
		}
		if CompareVersions(gwStatus.CurrentVersion, gwStatus.LatestVersion) < 0 {
			gwStatus.UpdateAvailable = true
		}
	}()

	go func() {
		defer wg.Done()
		if flavor == "zed" {
			spec, err := u.fetchZedPackageSpec(ctx, rtInfo.OS, rtInfo.Arch)
			if err != nil {
				acpStatus.CheckError = err.Error()
				return
			}
			acpStatus.LatestVersion = NormalizeVersionDisplay(spec.Version)
			if curACPVer == "" {
				acpStatus.UpdateAvailable = curACPPkgDigest == "" || !strings.EqualFold(curACPPkgDigest, spec.SHA256)
			} else if CompareVersions(curACPVer, acpStatus.LatestVersion) < 0 {
				acpStatus.UpdateAvailable = true
			}
			return
		}
		manifest, err := u.fetchCLIManifest(ctx, rtInfo.OS, rtInfo.Arch)
		if err != nil {
			acpStatus.CheckError = err.Error()
			return
		}
		acpStatus.LatestVersion = NormalizeVersionDisplay(manifest.Version)
		if curACPVer == "" {
			acpStatus.UpdateAvailable = curACPPkgDigest == "" || !strings.EqualFold(curACPPkgDigest, manifest.SHA512)
		} else if CompareVersions(curACPVer, acpStatus.LatestVersion) < 0 {
			acpStatus.UpdateAvailable = true
		}
	}()

	wg.Wait()

	out := StatusResponse{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Updating:  updating,
		Runtime:   rtInfo,
		Gateway:   gwStatus,
		ACP:       acpStatus,
	}

	u.mu.Lock()
	out.Updating = u.updating
	cp := out
	u.lastStatus = &cp
	u.lastCheckedAt = time.Now()
	u.mu.Unlock()
	return out
}

func (u *Updater) beginUpdate() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.updating {
		return ErrAlreadyUpdating{}
	}
	u.updating = true
	return nil
}

func (u *Updater) endUpdate() {
	u.mu.Lock()
	u.updating = false
	u.lastCheckedAt = time.Time{}
	u.mu.Unlock()
}

func (u *Updater) downloadToFileAndHash(ctx context.Context, rawURL, dstPath string, h hash.Hash, maxBytes int64) (string, error) {
	if err := u.validateRemoteURL(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Antigravity-ACP-Gateway/"+strings.TrimPrefix(version.Version, "v"))
	if tok := strings.TrimSpace(os.Getenv("AG_GITHUB_TOKEN")); tok != "" && u.isConfiguredGitHubHost(req.URL.Host) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/octet-stream")

	client := u.DownloadClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 返回 HTTP %d", rawURL, resp.StatusCode)
	}

	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	var writer io.Writer = f
	if h != nil {
		writer = io.MultiWriter(f, h)
	}
	n, copyErr := io.Copy(writer, limited)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return "", fmt.Errorf("写入下载文件失败: %w", copyErr)
	}
	if n > maxBytes {
		_ = os.Remove(dstPath)
		return "", fmt.Errorf("下载文件体积超过上限 (%d 字节)", maxBytes)
	}
	if syncErr != nil {
		_ = os.Remove(dstPath)
		return "", syncErr
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return "", closeErr
	}
	if h == nil {
		return "", nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func constantTimeHexEqual(actualHex, expectedHex string) bool {
	a := strings.ToLower(strings.TrimSpace(actualHex))
	b := strings.ToLower(strings.TrimSpace(expectedHex))
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Apply downloads and verifies the requested target ("gateway", "acp", or "all").
func (u *Updater) Apply(
	ctx context.Context,
	target string,
	backendType string,
	configuredBinPath string,
	onACPUpdated func(flavor, newBinPath string) int,
) (ApplyResult, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target != "gateway" && target != "acp" && target != "all" {
		return ApplyResult{}, fmt.Errorf("无效的更新目标 %q (支持 gateway | acp | all)", target)
	}
	if err := u.beginUpdate(); err != nil {
		return ApplyResult{}, err
	}
	defer u.endUpdate()

	var res ApplyResult

	if target == "acp" || target == "all" {
		ver, binPath, flavor, updated, err := u.applyACPInternal(ctx, backendType, configuredBinPath)
		if err != nil {
			return res, err
		}
		res.UpdatedACP = updated
		res.ACPVersion = ver
		res.ACPBinaryPath = binPath
		if updated && onACPUpdated != nil {
			res.RetiredWorkers = onACPUpdated(flavor, binPath)
		}
	}

	if target == "gateway" || target == "all" {
		ver, execPath, updated, err := u.applyGatewayInternal(ctx)
		if err != nil {
			return res, err
		}
		res.UpdatedGateway = updated
		res.GatewayVersion = ver
		res.ExecutablePath = execPath
		res.Restarting = updated
	}

	return res, nil
}

func (u *Updater) applyGatewayInternal(ctx context.Context) (string, string, bool, error) {
	rtInfo, canSelfUpdate, blockedReason, _ := u.detectRuntime()
	if !canSelfUpdate {
		return "", "", false, errors.New(blockedReason)
	}

	rel, found, err := u.fetchLatestGitHubRelease(ctx)
	if err != nil {
		return "", "", false, err
	}
	if !found || rel == nil {
		repo := strings.TrimSpace(u.Repo)
		if repo == "" {
			repo = version.EffectiveRepo()
		}
		return "", "", false, fmt.Errorf("远程仓库 %s 尚未发布任何 GitHub Release", repo)
	}

	latestVer := NormalizeVersionDisplay(rel.TagName)
	curVer := NormalizeVersionDisplay(version.Version)
	if st, err := os.Stat(rtInfo.ExecutablePath); err == nil && !st.IsDir() {
		if curVer != "" && !strings.EqualFold(curVer, "dev") && latestVer != "" && CompareVersions(curVer, latestVer) >= 0 {
			return curVer, rtInfo.ExecutablePath, false, nil
		}
	}

	wantAssetName := expectedGatewayAssetName(rtInfo.OS, rtInfo.Arch)
	archiveAsset, ok := findReleaseAsset(rel.Assets, wantAssetName)
	if !ok {
		return "", "", false, fmt.Errorf("Release %s 缺少当前平台预编译包 (%s)", rel.TagName, wantAssetName)
	}
	checksumAsset, ok := findChecksumAsset(rel.Assets)
	if !ok {
		return "", "", false, fmt.Errorf("Release %s 缺少 SHA256SUMS 校验文件，已拒绝安装", rel.TagName)
	}

	execDir := filepath.Dir(rtInfo.ExecutablePath)
	workDir, err := os.MkdirTemp(execDir, ".ag-gw-update-*")
	if err != nil {
		return "", "", false, fmt.Errorf("在网关目录 (%s) 创建临时更新工作区失败: %w", execDir, err)
	}
	defer os.RemoveAll(workDir)

	sumsPath := filepath.Join(workDir, "SHA256SUMS")
	if _, err := u.downloadToFileAndHash(ctx, releaseAssetDownloadURL(checksumAsset), sumsPath, nil, maxMetadataBytes); err != nil {
		return "", "", false, fmt.Errorf("下载 SHA256SUMS 失败: %w", err)
	}
	sumsBytes, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", "", false, err
	}
	expectedSHA256, err := parseChecksumForFile(string(sumsBytes), archiveAsset.Name)
	if err != nil {
		return "", "", false, err
	}

	archivePath := filepath.Join(workDir, archiveAsset.Name)
	actualSHA256, err := u.downloadToFileAndHash(ctx, releaseAssetDownloadURL(archiveAsset), archivePath, sha256.New(), maxArchiveBytes)
	if err != nil {
		return "", "", false, err
	}
	if !constantTimeHexEqual(actualSHA256, expectedSHA256) {
		return "", "", false, fmt.Errorf("网关预编译包 SHA-256 校验失败: 预期 %s，实际 %s", expectedSHA256, actualSHA256)
	}

	gwBinName := expectedGatewayBinaryName(rtInfo.OS)
	probeBinName := expectedProbeBinaryName(rtInfo.OS)
	wantFiles := map[string]string{
		gwBinName: filepath.Join(workDir, gwBinName+".extracted"),
	}
	liveProbePath := filepath.Join(execDir, probeBinName)
	updateProbe := false
	if st, err := os.Stat(liveProbePath); err == nil && !st.IsDir() {
		wantFiles[probeBinName] = filepath.Join(workDir, probeBinName+".extracted")
		updateProbe = true
	}

	if strings.HasSuffix(strings.ToLower(archiveAsset.Name), ".zip") {
		if err := extractFilesFromZip(archivePath, wantFiles); err != nil {
			return "", "", false, err
		}
	} else {
		if err := extractFilesFromTarGz(archivePath, wantFiles); err != nil {
			return "", "", false, err
		}
	}

	extractedGW := wantFiles[gwBinName]
	if _, err := os.Stat(extractedGW); err != nil {
		return "", "", false, fmt.Errorf("预编译包中未找到主程序二进制 %s", gwBinName)
	}
	if err := VerifyExecutableBinary(extractedGW, rtInfo.OS, rtInfo.Arch); err != nil {
		return "", "", false, fmt.Errorf("提取的网关二进制未通过平台可执行文件头校验: %w", err)
	}

	if updateProbe {
		extractedProbe := wantFiles[probeBinName]
		if _, err := os.Stat(extractedProbe); err == nil {
			if VerifyExecutableBinary(extractedProbe, rtInfo.OS, rtInfo.Arch) == nil {
				_ = replaceExecutableFile(extractedProbe, liveProbePath)
			}
		}
	}

	if err := replaceExecutableFile(extractedGW, rtInfo.ExecutablePath); err != nil {
		return "", "", false, fmt.Errorf("替换网关主程序失败: %w", err)
	}

	return latestVer, rtInfo.ExecutablePath, true, nil
}

func (u *Updater) applyACPInternal(ctx context.Context, backendType, configuredBinPath string) (string, string, string, bool, error) {
	goos := u.effectiveGOOS()
	goarch := u.effectiveGOARCH()
	resolvedACP := acp.ResolveACPBinaryPath(backendType, configuredBinPath)
	flavor := determineACPFlavor(backendType, resolvedACP)

	localExists := false
	if resolvedACP != "" {
		if st, err := os.Stat(resolvedACP); err == nil && !st.IsDir() {
			localExists = true
		}
	}
	curACPVer, curACPPkgDigest := detectLocalACPVersion(ctx, flavor, resolvedACP, u.loadStoredACPMeta())

	installDir, canHotUpdate, blockedReason := u.resolveACPInstallDir(resolvedACP)
	if !canHotUpdate || installDir == "" {
		return "", "", flavor, false, errors.New(blockedReason)
	}

	if flavor == "zed" {
		spec, err := u.fetchZedPackageSpec(ctx, goos, goarch)
		if err != nil {
			return "", "", flavor, false, err
		}
		latestVer := NormalizeVersionDisplay(spec.Version)
		if localExists {
			if curACPVer != "" && latestVer != "" && CompareVersions(curACPVer, latestVer) >= 0 {
				return curACPVer, resolvedACP, "zed", false, nil
			}
			if curACPVer == "" && curACPPkgDigest != "" && strings.EqualFold(curACPPkgDigest, spec.SHA256) {
				return latestVer, resolvedACP, "zed", false, nil
			}
		}

		workDir, err := os.MkdirTemp(installDir, ".ag-acp-update-*")
		if err != nil {
			return "", "", flavor, false, fmt.Errorf("在 %s 创建临时更新目录失败: %w", installDir, err)
		}
		defer os.RemoveAll(workDir)

		zipPath := filepath.Join(workDir, "zed-acp.zip")
		actualSHA256, err := u.downloadToFileAndHash(ctx, spec.ArchiveURL, zipPath, sha256.New(), maxArchiveBytes)
		if err != nil {
			return "", "", flavor, false, err
		}
		if !constantTimeHexEqual(actualSHA256, spec.SHA256) {
			return "", "", flavor, false, fmt.Errorf("Zed ACP 包 SHA-256 校验失败: 预期 %s，实际 %s", spec.SHA256, actualSHA256)
		}

		candidates := []string{
			"agy_acp_server.par",
			"agy_acp_server",
			"agy_acp_server.exe",
			"localharness_external",
			"localharness_external.exe",
		}
		wantMap := make(map[string]string, len(candidates))
		for _, fn := range candidates {
			wantMap[fn] = filepath.Join(workDir, fn+".extracted")
		}
		if err := extractFilesFromZip(zipPath, wantMap); err != nil {
			return "", "", flavor, false, err
		}

		var primaryInstalled string
		for _, fn := range candidates {
			extracted := wantMap[fn]
			if _, err := os.Stat(extracted); err != nil {
				continue
			}
			destPath := filepath.Join(installDir, fn)
			if err := replaceExecutableFile(extracted, destPath); err != nil {
				return "", "", flavor, false, fmt.Errorf("安装 Zed ACP 组件 %s 失败: %w", fn, err)
			}
			if primaryInstalled == "" && strings.HasPrefix(fn, "agy_acp_server") {
				primaryInstalled = destPath
			}
		}
		if primaryInstalled == "" {
			return "", "", flavor, false, fmt.Errorf("Zed ACP 压缩包中未找到可执行的 agy_acp_server 组件")
		}

		fileSHA, _ := computeFileDigestHex(primaryInstalled, sha256.New())
		ver := latestVer
		_ = os.WriteFile(
			filepath.Join(installDir, ".zed-acp-version"),
			[]byte(fmt.Sprintf("%s %s\n", strings.TrimPrefix(ver, "v"), spec.SHA256)),
			0644,
		)
		u.saveStoredACPMeta(installedACPMeta{
			Flavor:        "zed",
			Version:       ver,
			BinaryPath:    primaryInstalled,
			FileSHA256:    fileSHA,
			PackageDigest: spec.SHA256,
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		})
		if u.Store != nil {
			_ = u.Store.SetSetting(SettingACPOverrideBinPath, primaryInstalled)
		}
		return ver, primaryInstalled, "zed", true, nil
	}

	// Default flavor: Antigravity CLI ("cli")
	manifest, err := u.fetchCLIManifest(ctx, goos, goarch)
	if err != nil {
		return "", "", flavor, false, err
	}
	latestVer := NormalizeVersionDisplay(manifest.Version)
	if localExists {
		if curACPVer != "" && latestVer != "" && CompareVersions(curACPVer, latestVer) >= 0 {
			return curACPVer, resolvedACP, "cli", false, nil
		}
		if curACPVer == "" && curACPPkgDigest != "" && strings.EqualFold(curACPPkgDigest, manifest.SHA512) {
			return latestVer, resolvedACP, "cli", false, nil
		}
	}

	workDir, err := os.MkdirTemp(installDir, ".ag-acp-update-*")
	if err != nil {
		return "", "", flavor, false, fmt.Errorf("在 %s 创建临时更新目录失败: %w", installDir, err)
	}
	defer os.RemoveAll(workDir)

	pkgPath := filepath.Join(workDir, "agy_pkg")
	actualSHA512, err := u.downloadToFileAndHash(ctx, manifest.URL, pkgPath, sha512.New(), maxArchiveBytes)
	if err != nil {
		return "", "", flavor, false, err
	}
	if !constantTimeHexEqual(actualSHA512, manifest.SHA512) {
		return "", "", flavor, false, fmt.Errorf("Antigravity CLI SHA-512 校验失败: 预期 %s，实际 %s", manifest.SHA512, actualSHA512)
	}

	lowerURL := strings.ToLower(manifest.URL)
	extractedCLI := filepath.Join(workDir, "agy.extracted")
	if strings.Contains(lowerURL, ".tar.gz") || strings.Contains(lowerURL, ".tgz") {
		want := map[string]string{
			"antigravity":     extractedCLI,
			"agy":             extractedCLI,
			"antigravity.exe": extractedCLI,
			"agy.exe":         extractedCLI,
		}
		if err := extractFilesFromTarGz(pkgPath, want); err != nil {
			return "", "", flavor, false, err
		}
	} else if strings.Contains(lowerURL, ".zip") {
		want := map[string]string{
			"antigravity":     extractedCLI,
			"agy":             extractedCLI,
			"antigravity.exe": extractedCLI,
			"agy.exe":         extractedCLI,
		}
		if err := extractFilesFromZip(pkgPath, want); err != nil {
			return "", "", flavor, false, err
		}
	} else {
		extractedCLI = pkgPath
	}

	if _, err := os.Stat(extractedCLI); err != nil {
		return "", "", flavor, false, fmt.Errorf("CLI 安装包中未找到可执行文件")
	}
	if err := VerifyExecutableBinary(extractedCLI, goos, goarch); err != nil {
		return "", "", flavor, false, fmt.Errorf("下载的 Antigravity CLI 二进制未通过平台文件头校验: %w", err)
	}

	binName := "agy"
	if goos == "windows" {
		binName = "agy.exe"
	}
	destBin := filepath.Join(installDir, binName)
	if err := replaceExecutableFile(extractedCLI, destBin); err != nil {
		return "", "", flavor, false, fmt.Errorf("写入 Antigravity CLI 到 %s 失败: %w", destBin, err)
	}

	fileSHA, _ := computeFileDigestHex(destBin, sha256.New())
	ver := latestVer
	u.saveStoredACPMeta(installedACPMeta{
		Flavor:        "cli",
		Version:       ver,
		BinaryPath:    destBin,
		FileSHA256:    fileSHA,
		PackageDigest: manifest.SHA512,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	})
	if u.Store != nil {
		_ = u.Store.SetSetting(SettingACPOverrideBinPath, destBin)
	}
	return ver, destBin, "cli", true, nil
}

func extractFilesFromTarGz(archivePath string, wantByBaseName map[string]string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压 gzip 失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	written := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 归档失败: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(strings.ReplaceAll(hdr.Name, `\`, `/`))
		dst, ok := wantByBaseName[base]
		if !ok || written[dst] {
			continue
		}
		if err := writeBoundedExtractedFile(tr, dst, maxArchiveBytes); err != nil {
			return err
		}
		written[dst] = true
	}
	return nil
}

func extractFilesFromZip(archivePath string, wantByBaseName map[string]string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("打开 zip 归档失败: %w", err)
	}
	defer zr.Close()

	written := make(map[string]bool)
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(strings.ReplaceAll(zf.Name, `\`, `/`))
		dst, ok := wantByBaseName[base]
		if !ok || written[dst] {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		wErr := writeBoundedExtractedFile(rc, dst, maxArchiveBytes)
		_ = rc.Close()
		if wErr != nil {
			return wErr
		}
		written[dst] = true
	}
	return nil
}

func writeBoundedExtractedFile(r io.Reader, dst string, maxBytes int64) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(r, maxBytes+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if n > maxBytes {
		_ = os.Remove(dst)
		return fmt.Errorf("解压文件超过最大长度限制 (%d 字节)", maxBytes)
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return syncErr
	}
	return closeErr
}

// VerifyExecutableBinary validates OS & CPU architecture headers using Go's debug/{elf,macho,pe} parsers.
func VerifyExecutableBinary(path, goos, goarch string) error {
	switch goos {
	case "linux":
		ef, err := elf.Open(path)
		if err != nil {
			return fmt.Errorf("无效的 Linux ELF 二进制: %w", err)
		}
		defer ef.Close()
		switch goarch {
		case "amd64":
			if ef.Machine != elf.EM_X86_64 {
				return fmt.Errorf("ELF 架构不匹配: 预期 amd64 (EM_X86_64)，实际 %v", ef.Machine)
			}
		case "arm64":
			if ef.Machine != elf.EM_AARCH64 {
				return fmt.Errorf("ELF 架构不匹配: 预期 arm64 (EM_AARCH64)，实际 %v", ef.Machine)
			}
		}
		return nil
	case "darwin":
		if mf, err := macho.Open(path); err == nil {
			defer mf.Close()
			return verifyMachOCPU(mf.Cpu, goarch)
		}
		ff, err := macho.OpenFat(path)
		if err != nil {
			return fmt.Errorf("无效的 macOS Mach-O 二进制: %w", err)
		}
		defer ff.Close()
		for _, arch := range ff.Arches {
			if verifyMachOCPU(arch.Cpu, goarch) == nil {
				return nil
			}
		}
		return fmt.Errorf("Mach-O 通用二进制中不包含 %s 架构切片", goarch)
	case "windows":
		pf, err := pe.Open(path)
		if err != nil {
			return fmt.Errorf("无效的 Windows PE 二进制: %w", err)
		}
		defer pf.Close()
		switch goarch {
		case "amd64":
			if pf.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
				return fmt.Errorf("PE 架构不匹配: 预期 amd64，实际 0x%x", pf.Machine)
			}
		case "arm64":
			if pf.Machine != pe.IMAGE_FILE_MACHINE_ARM64 {
				return fmt.Errorf("PE 架构不匹配: 预期 arm64，实际 0x%x", pf.Machine)
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持校验的操作系统: %s", goos)
	}
}

func verifyMachOCPU(cpu macho.Cpu, goarch string) error {
	switch goarch {
	case "amd64":
		if cpu != macho.CpuAmd64 {
			return fmt.Errorf("Mach-O 架构不匹配: 预期 amd64，实际 %v", cpu)
		}
	case "arm64":
		if cpu != macho.CpuArm64 {
			return fmt.Errorf("Mach-O 架构不匹配: 预期 arm64，实际 %v", cpu)
		}
	}
	return nil
}

// NormalizeVersionDisplay ensures semantic versions carry a leading "v" (e.g. "1.6.0" -> "v1.6.0").
func NormalizeVersionDisplay(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if s[0] >= '0' && s[0] <= '9' {
		return "v" + s
	}
	if strings.HasPrefix(s, "V") {
		return "v" + s[1:]
	}
	return s
}

type parsedSemver struct {
	major int
	minor int
	patch int
	pre   string
	valid bool
}

func parseSemver(raw string) parsedSemver {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		s = s[:idx]
	}
	var pre string
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		pre = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return parsedSemver{}
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return parsedSemver{}
	}
	pat := 0
	if len(parts) == 3 {
		p, err3 := strconv.Atoi(parts[2])
		if err3 != nil {
			return parsedSemver{}
		}
		pat = p
	}
	return parsedSemver{major: maj, minor: min, patch: pat, pre: pre, valid: true}
}

// CompareVersions returns -1 if a < b, 0 if a == b, and +1 if a > b.
func CompareVersions(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	if !pa.valid || !pb.valid {
		na := NormalizeVersionDisplay(a)
		nb := NormalizeVersionDisplay(b)
		if na == nb {
			return 0
		}
		if na < nb {
			return -1
		}
		return 1
	}
	if pa.major != pb.major {
		if pa.major < pb.major {
			return -1
		}
		return 1
	}
	if pa.minor != pb.minor {
		if pa.minor < pb.minor {
			return -1
		}
		return 1
	}
	if pa.patch != pb.patch {
		if pa.patch < pb.patch {
			return -1
		}
		return 1
	}
	if pa.pre == pb.pre {
		return 0
	}
	// A final release is newer than a prerelease of the same version (e.g. 1.6.0 > 1.6.0-rc.1),
	// except local "-dirty" builds which represent uncommitted edits on top of the version.
	if pa.pre == "dirty" && pb.pre == "" {
		return 0
	}
	if pa.pre == "" && pb.pre != "" {
		return 1
	}
	if pa.pre != "" && pb.pre == "" {
		return -1
	}
	if pa.pre < pb.pre {
		return -1
	}
	return 1
}
