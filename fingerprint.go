package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// The following constants and tables replicate commandcode-proxy-master's
// deterministic device fingerprint derivation (command-code@1.53.1 wire shape).
const (
	fingerprintSalt = "command-code:device-fingerprint:v1"
)

type fingerprintCPU struct {
	model string
	cores int
}

var fingerprintCPUs = []fingerprintCPU{
	{model: "12th Gen Intel(R) Core(TM) i7-12650H", cores: 10},
	{model: "12th Gen Intel(R) Core(TM) i5-12400F", cores: 6},
	{model: "12th Gen Intel(R) Core(TM) i9-12900K", cores: 16},
	{model: "13th Gen Intel(R) Core(TM) i7-13700K", cores: 16},
	{model: "13th Gen Intel(R) Core(TM) i5-13600K", cores: 14},
	{model: "13th Gen Intel(R) Core(TM) i9-13900K", cores: 24},
	{model: "Intel(R) Core(TM) Ultra 7 155H", cores: 16},
	{model: "Intel(R) Core(TM) Ultra 9 285H", cores: 16},
	{model: "Intel(R) Core(TM) i9-14900K", cores: 24},
	{model: "Intel(R) Core(TM) i7-14700K", cores: 20},
	{model: "AMD Ryzen 7 7800X3D", cores: 8},
	{model: "AMD Ryzen 9 7950X", cores: 16},
	{model: "AMD Ryzen 5 7600", cores: 6},
	{model: "AMD Ryzen 9 7900X", cores: 12},
	{model: "AMD Ryzen 7 5800X3D", cores: 8},
}

var fingerprintMemoryGiBs = []int{8, 16, 24, 32, 48, 64}

var fingerprintTimezones = []string{
	"America/New_York", "America/Chicago", "America/Los_Angeles", "America/Toronto",
	"Europe/London", "Europe/Berlin", "Europe/Paris", "Europe/Moscow",
	"Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "Asia/Seoul", "Asia/Hong_Kong",
	"Australia/Sydney", "Pacific/Auckland",
}

var fingerprintMACCountRange = []int{2, 3, 4, 5}

var fingerprintOSUsers = []string{"dev", "user", "admin", "coder", "engineer", "work"}

var fingerprintMailDomains = []string{"gmail.com", "outlook.com", "qq.com", "163.com"}

type deviceProfile struct {
	platform    string
	arch        string
	osRelease   string
	isContainer bool
	projectDir  string
}

func defaultDeviceProfile(cfg *pluginConfig) deviceProfile {
	dir := defaultDeviceProjectDir
	platform := "win32"
	if cfg != nil && strings.TrimSpace(cfg.DeviceProjectDir) != "" {
		dir = strings.TrimSpace(cfg.DeviceProjectDir)
	}
	return deviceProfile{
		platform:    platform,
		arch:        "x64",
		osRelease:   "10.0.22631",
		isContainer: false,
		projectDir:  dir,
	}
}

func fpDigest(apiKey, field string) []byte {
	return fpDigestWithSalt(apiKey, field, "")
}

func fpDigestWithSalt(apiKey, field, salt string) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte(salt + "\x00" + apiKey + "\x00" + field))
	return h.Sum(nil)
}

func fpPickIndex(apiKey, field string, items []string) int {
	bestIdx := 0
	bestScore := []byte(nil)
	for idx, item := range items {
		score := fpDigest(apiKey, field+"\x00"+item)
		if bestScore == nil || bytes.Compare(score, bestScore) > 0 {
			bestScore = score
			bestIdx = idx
		}
	}
	return bestIdx
}

func fingerprintHash(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fingerprintSalt + "\x00" + strings.ToLower(v)))
	return hex.EncodeToString(sum[:])
}

func hexBytes(data []byte) string { return hex.EncodeToString(data) }

func generateFingerprint(apiKey string, cfg *pluginConfig) map[string]any {
	salt := ""
	if cfg != nil {
		salt = cfg.FingerprintSalt
	}
	digestFor := func(field string) []byte {
		return fpDigestWithSalt(apiKey, field, salt)
	}
	pick := func(field string, items []string) int {
		bestIdx := 0
		bestScore := []byte(nil)
		for idx, item := range items {
			score := digestFor(field + "\x00" + item)
			if bestScore == nil || bytes.Compare(score, bestScore) > 0 {
				bestScore = score
				bestIdx = idx
			}
		}
		return bestIdx
	}
	cpuIdx := 0
	cpuBest := []byte(nil)
	for idx, cpu := range fingerprintCPUs {
		score := digestFor("cpu\x00" + cpu.model + "|" + itoa(int64(cpu.cores)))
		if cpuBest == nil || bytes.Compare(score, cpuBest) > 0 {
			cpuBest = score
			cpuIdx = idx
		}
	}
	cpu := fingerprintCPUs[cpuIdx]
	memGiB := fingerprintMemoryGiBs[pick("mem", intList(fingerprintMemoryGiBs))]
	timezone := fingerprintTimezones[pick("timezone", fingerprintTimezones)]
	macCount := fingerprintMACCountRange[pick("macCount", intList(fingerprintMACCountRange))]
	osUser := fingerprintOSUsers[pick("osUser", fingerprintOSUsers)]
	mailDomain := fingerprintMailDomains[pick("mailDomain", fingerprintMailDomains)]
	hexField := func(field string, n int) string {
		return hex.EncodeToString(digestFor(field)[:n])
	}

	machineID := hexField("machineId", 16)
	machineID = machineID[:8] + "-" + machineID[8:12] + "-" + machineID[12:16] + "-" + machineID[16:20] + "-" + machineID[20:32]
	macs := make([]string, 0, macCount)
	for i := 0; i < macCount; i++ {
		b := digestFor("mac" + itoa(int64(i)))
		parts := make([]string, 6)
		for j := 0; j < 6; j++ {
			parts[j] = hex.EncodeToString([]byte{b[j]})
		}
		macs = append(macs, strings.Join(parts, ":"))
	}
	sort.Strings(macs)
	hostname := "DESKTOP-" + strings.ToUpper(hexField("hostname", 4))
	gitEmail := osUser + "." + hexField("gitEmail", 3) + "@" + mailDomain

	machineIDHash := fingerprintHash(machineID)
	macHashes := make([]string, 0, len(macs))
	for _, mac := range macs {
		if hash := fingerprintHash(mac); hash != "" {
			macHashes = append(macHashes, hash)
		}
	}
	osUserHash := fingerprintHash(osUser)
	hostnameHash := fingerprintHash(hostname)
	gitEmailHash := fingerprintHash(gitEmail)

	thumbParts := make([]string, 0, 4)
	thumbParts = append(thumbParts, strings.TrimSpace(machineID), strings.Join(macs, ","))
	if strings.TrimSpace(machineID) == "" {
		thumbParts = append(thumbParts, hostname, cpu.model)
	}
	thumbSeed := strings.Join(thumbParts, "|")
	if thumbSeed == "" {
		thumbSeed = "unknown"
	}
	thumbmarkSum := sha256.Sum256([]byte(fingerprintSalt + "\x00machine\x00" + thumbSeed))
	thumbmark := hex.EncodeToString(thumbmarkSum[:])

	components := map[string]any{
		"platform":         defaultDeviceProfile(cfg).platform,
		"arch":             defaultDeviceProfile(cfg).arch,
		"osRelease":        defaultDeviceProfile(cfg).osRelease,
		"cpuModel":         cpu.model,
		"cpuCount":         cpu.cores,
		"memGiB":           memGiB,
		"isContainer":      defaultDeviceProfile(cfg).isContainer,
		"timezone":         timezone,
		"runtime":          "cli",
		"collectorVersion": 1,
	}
	if machineIDHash != "" {
		components["machineIdHash"] = machineIDHash
	}
	if len(macHashes) > 0 {
		components["macHashes"] = macHashes
	}
	if osUserHash != "" {
		components["osUserHash"] = osUserHash
	}
	if hostnameHash != "" {
		components["hostnameHash"] = hostnameHash
	}
	if gitEmailHash != "" {
		components["gitEmailHash"] = gitEmailHash
	}
	return map[string]any{
		"thumbmark":  thumbmark,
		"components": components,
	}
}

func intList(values []int) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, itoa(int64(v)))
	}
	return out
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
